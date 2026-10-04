package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

const sampleRate = 16000

func sherpaThreads() int {
	n := runtime.NumCPU()
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	return n
}

// Default clustering threshold. Lower = more clusters. We deliberately err
// towards too many clusters: splitting one person into two clusters is fixed
// later by voice matching (both map to the same name), but two people merged
// into one cluster can't be separated again.
const defaultDiarizeThreshold = 0.5

// DiarizeOpts are the user-tunable speaker detection settings.
type DiarizeOpts struct {
	NumSpeakers int
	Threshold   float32
	Model       string  // key in diarizeModels
	Step        float32 // window shift ratio: 0.1 = 1 s steps on the 10 s window
}

func (o DiarizeOpts) String() string {
	return fmt.Sprintf("%s, step %.1fs, threshold %.2f", o.Model, o.Step*10, o.Threshold)
}

func diarizationConfig(o DiarizeOpts) *sherpa.OfflineSpeakerDiarizationConfig {
	numSpeakers, threshold := o.NumSpeakers, o.Threshold
	c := &sherpa.OfflineSpeakerDiarizationConfig{}
	c.Segmentation.Pyannote.Model = segmentationModelPath()
	c.Segmentation.Pyannote.WindowShiftRatio = o.Step
	c.Segmentation.NumThreads = sherpaThreads()
	c.Segmentation.Provider = inProcessProvider()
	c.Embedding.Model = diarizeModelPath(o.Model)
	c.Embedding.NumThreads = sherpaThreads()
	c.Embedding.Provider = inProcessProvider()
	if numSpeakers > 0 {
		c.Clustering.NumClusters = numSpeakers
	} else {
		c.Clustering.NumClusters = -1
		c.Clustering.Threshold = threshold
	}
	c.MinDurationOn = 0.3
	c.MinDurationOff = 0.5
	return c
}

func checkDiarizationModels() error {
	sd := sherpa.NewOfflineSpeakerDiarization(diarizationConfig(DiarizeOpts{Threshold: defaultDiarizeThreshold, Model: defaultDiarizeModel, Step: defaultDiarizeStep}))
	if sd == nil {
		return fmt.Errorf("could not load segmentation/embedding models")
	}
	defer sherpa.DeleteOfflineSpeakerDiarization(sd)
	if sd.SampleRate() != sampleRate {
		return fmt.Errorf("unexpected model sample rate %d", sd.SampleRate())
	}
	return nil
}

// readWav16k reads the 16 kHz mono 16-bit PCM WAV that ffmpeg produced.
func readWav16k(path string) ([]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return nil, err
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, fmt.Errorf("%s is not a WAV file", path)
	}
	var fmtOK bool
	for {
		var ch [8]byte
		if _, err := io.ReadFull(f, ch[:]); err != nil {
			return nil, fmt.Errorf("no data chunk in WAV: %w", err)
		}
		id := string(ch[0:4])
		size := int64(binary.LittleEndian.Uint32(ch[4:8]))
		switch id {
		case "fmt ":
			b := make([]byte, size)
			if _, err := io.ReadFull(f, b); err != nil {
				return nil, err
			}
			format := binary.LittleEndian.Uint16(b[0:2])
			chans := binary.LittleEndian.Uint16(b[2:4])
			rate := binary.LittleEndian.Uint32(b[4:8])
			bits := binary.LittleEndian.Uint16(b[14:16])
			if format != 1 || chans != 1 || rate != sampleRate || bits != 16 {
				return nil, fmt.Errorf("WAV must be PCM 16-bit mono 16 kHz (got fmt=%d ch=%d rate=%d bits=%d)", format, chans, rate, bits)
			}
			fmtOK = true
		case "data":
			if !fmtOK {
				return nil, fmt.Errorf("WAV data before fmt chunk")
			}
			// ffmpeg writes a correct size for files; fall back to "read to end" if not.
			var raw []byte
			if size > 0 && size < 0xFFFFFFF0 {
				raw = make([]byte, size)
				n, err := io.ReadFull(f, raw)
				if err != nil && err != io.ErrUnexpectedEOF {
					return nil, err
				}
				raw = raw[:n]
			} else {
				raw, err = io.ReadAll(f)
				if err != nil {
					return nil, err
				}
			}
			out := make([]float32, len(raw)/2)
			for i := range out {
				out[i] = float32(int16(binary.LittleEndian.Uint16(raw[2*i:]))) / 32768
			}
			return out, nil
		default:
			if _, err := f.Seek(size+size%2, io.SeekCurrent); err != nil {
				return nil, err
			}
		}
	}
}

// diarize runs speaker diarization over the whole episode and computes one
// voice embedding per detected speaker cluster (used later to match clusters
// to known people across episodes).
func diarize(samples []float32, o DiarizeOpts) ([]Turn, []ClusterEmbedding, error) {
	c := diarizationConfig(o)
	release := gpuGuard(c.Embedding.Provider)
	defer func() { release() }()
	sd := sherpa.NewOfflineSpeakerDiarization(c)
	if sd == nil && c.Embedding.Provider != "cpu" {
		logf("   speaker detection could not start on the graphics card - using the CPU")
		c.Segmentation.Provider, c.Embedding.Provider = "cpu", "cpu"
		release()
		sd = sherpa.NewOfflineSpeakerDiarization(c)
	}
	if sd == nil {
		return nil, nil, fmt.Errorf("could not load diarization models")
	}
	defer sherpa.DeleteOfflineSpeakerDiarization(sd)
	if len(samples) == 0 {
		return nil, nil, fmt.Errorf("no audio samples")
	}

	segs := sd.Process(samples)
	turns := make([]Turn, 0, len(segs))
	for _, s := range segs {
		turns = append(turns, Turn{
			StartMs: int64(s.Start * 1000),
			EndMs:   int64(s.End * 1000),
			Cluster: s.Speaker,
		})
	}
	sort.Slice(turns, func(i, j int) bool { return turns[i].StartMs < turns[j].StartMs })
	// sherpa's cluster ids can have gaps (0,1,3,5...); renumber by first appearance
	remap := map[int]int{}
	for i := range turns {
		n, ok := remap[turns[i].Cluster]
		if !ok {
			n = len(remap)
			remap[turns[i].Cluster] = n
		}
		turns[i].Cluster = n
	}

	embs, err := clusterEmbeddings(samples, turns)
	if err != nil {
		// Not fatal: the transcript is still useful, speaker matching just
		// can't use this version until it is re-diarized.
		logf("  warning: could not compute speaker embeddings: %v", err)
	}
	return turns, embs, nil
}

const (
	embedMaxSeconds  = 30.0 // audio per cluster used for its voiceprint
	embedMaxTurnSecs = 10.0 // max piece taken from one turn
	embedMinTurnSecs = 1.0  // ignore very short turns (often clipped or crosstalk)
)

func clusterEmbeddings(samples []float32, turns []Turn) ([]ClusterEmbedding, error) {
	prov := inProcessProvider()
	defer gpuGuard(prov)()
	ex := sherpa.NewSpeakerEmbeddingExtractor(&sherpa.SpeakerEmbeddingExtractorConfig{
		Model:      embeddingModelPath(),
		NumThreads: sherpaThreads(),
		Provider:   prov,
	})
	if ex == nil {
		return nil, fmt.Errorf("could not load embedding model")
	}
	defer sherpa.DeleteSpeakerEmbeddingExtractor(ex)

	overlaps := overlapRegions(turns)
	byCluster := map[int][]Turn{}
	allByCluster := map[int][]Turn{}
	for _, t := range turns {
		allByCluster[t.Cluster] = append(allByCluster[t.Cluster], t)
	}
	for _, t := range turns {
		// only clean turns: no time shared with another speaker
		if overlapWith(overlaps, t.StartMs, t.EndMs) > 0 {
			continue
		}
		if float64(t.EndMs-t.StartMs)/1000 < embedMinTurnSecs {
			continue
		}
		byCluster[t.Cluster] = append(byCluster[t.Cluster], t)
	}

	var out []ClusterEmbedding
	// clusters with no clean turn (only short or overlapping bits) still get a
	// voiceprint from whatever they have, so they can be merged later
	for c, ts := range allByCluster {
		if len(byCluster[c]) == 0 {
			byCluster[c] = ts
		}
	}
	clusters := make([]int, 0, len(byCluster))
	for c := range byCluster {
		clusters = append(clusters, c)
	}
	sort.Ints(clusters)
	for _, c := range clusters {
		ts := byCluster[c]
		// longest turns first - they are the cleanest samples of a voice
		sort.Slice(ts, func(i, j int) bool { return ts[i].EndMs-ts[i].StartMs > ts[j].EndMs-ts[j].StartMs })
		stream := ex.CreateStream()
		total := 0.0
		for _, t := range ts {
			if total >= embedMaxSeconds {
				break
			}
			secs := float64(t.EndMs-t.StartMs) / 1000
			if secs > embedMaxTurnSecs {
				secs = embedMaxTurnSecs
			}
			if total+secs > embedMaxSeconds {
				secs = embedMaxSeconds - total
			}
			a := int(t.StartMs * sampleRate / 1000)
			b := a + int(secs*sampleRate)
			if a < 0 || b > len(samples) || b <= a {
				continue
			}
			stream.AcceptWaveform(sampleRate, samples[a:b])
			total += secs
		}
		stream.InputFinished()
		if total > 0 && ex.IsReady(stream) {
			out = append(out, ClusterEmbedding{
				Cluster: c,
				Seconds: total,
				Model:   embeddingFile,
				Vec:     ex.Compute(stream),
			})
		}
		sherpa.DeleteOnlineStream(stream)
	}
	return out, nil
}
