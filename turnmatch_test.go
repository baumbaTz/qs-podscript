package main

import (
	"bufio"
	"fmt"
	"os"
	"testing"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// Per-turn matching against known clean voiceprints vs sherpa's clusters.
func TestTurnMatching(t *testing.T) {
	if os.Getenv("MODELS") == "" {
		t.Skip()
	}
	P.Models = os.Getenv("MODELS")
	s, _ := readWav16k("/tmp/pod.wav")
	type seg struct {
		a, b int64
		spk  int
	}
	var truth []seg
	f, _ := os.Open("/tmp/pod_truth.txt")
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var a, b float64
		var k int
		fmt.Sscanf(sc.Text(), "%f %f %d", &a, &b, &k)
		truth = append(truth, seg{int64(a * 1000), int64(b * 1000), k})
	}
	f.Close()
	trueAt := func(a, b int64) (int, int64) { // majority true speaker in [a,b]
		m := map[int]int64{}
		for _, g := range truth {
			if lo, hi := max64(a, g.a), min64(b, g.b); hi > lo {
				m[g.spk] += hi - lo
			}
		}
		best, bv := -1, int64(0)
		for k, v := range m {
			if v > bv {
				best, bv = k, v
			}
		}
		return best, bv
	}
	ex := sherpa.NewSpeakerEmbeddingExtractor(&sherpa.SpeakerEmbeddingExtractorConfig{Model: embeddingModelPath(), NumThreads: 1, Provider: "cpu"})
	emb := func(a, b int64) []float64 {
		st := ex.CreateStream()
		st.AcceptWaveform(16000, s[a*16:b*16])
		st.InputFinished()
		v := normalize(ex.Compute(st))
		sherpa.DeleteOnlineStream(st)
		return v
	}
	// "known voices": reference from the first 3 long utterances of each speaker
	// (like a user confirming a few passages)
	ref := map[int][]float64{}
	cnt := map[int]int{}
	for _, g := range truth {
		if g.b-g.a > 3000 && cnt[g.spk] < 3 {
			v := emb(g.a, g.b)
			if ref[g.spk] == nil {
				ref[g.spk] = make([]float64, len(v))
			}
			for i := range v {
				ref[g.spk][i] += v[i]
			}
			cnt[g.spk]++
		}
	}
	for _, cfg := range []DiarizeOpts{
		{Model: "resnet34", Step: 0.1, Threshold: 0.5},
		{Model: "resnet34", Step: 0.25, Threshold: 0.5},
		{Model: "resnet34", Step: 0.25, Threshold: 0.3},
	} {
		turns, _, _ := diarize(s, cfg)
		// A) cluster approach: each cluster -> its majority true speaker (best case naming)
		cl := map[int]map[int]int64{}
		for _, tu := range turns {
			if cl[tu.Cluster] == nil {
				cl[tu.Cluster] = map[int]int64{}
			}
			k, v := trueAt(tu.StartMs, tu.EndMs)
			if k >= 0 {
				cl[tu.Cluster][k] += v
			}
		}
		name := map[int]int{}
		for c, m := range cl {
			best, bv := -1, int64(0)
			for k, v := range m {
				if v > bv {
					best, bv = k, v
				}
			}
			name[c] = best
		}
		var total, okA, okB int64
		for _, tu := range turns {
			k, v := trueAt(tu.StartMs, tu.EndMs)
			total += v
			if name[tu.Cluster] == k {
				okA += v
			}
			// B) per-turn matching against known voices
			guess := name[tu.Cluster] // too short -> keep cluster's name
			if tu.EndMs-tu.StartMs >= 1500 {
				e := emb(tu.StartMs, tu.EndMs)
				best, bs := -1, -2.0
				for spk, r := range ref {
					if c := cosine(e, r); c > bs {
						best, bs = spk, c
					}
				}
				guess = best
			}
			if guess == k {
				okB += v
			}
		}
		fmt.Printf("%s: clusters named by majority %.1f%% correct | per-turn voice matching %.1f%% correct\n",
			cfg, float64(okA)*100/float64(total), float64(okB)*100/float64(total))
	}
	sherpa.DeleteSpeakerEmbeddingExtractor(ex)
}
