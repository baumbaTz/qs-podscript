package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// Speaker identification (phase 3). Instead of trusting sherpa's voice groups
// (which mix people), every speaker turn is compared with the voiceprints of
// known people, built from passages the user confirmed. Measured on test
// audio this is clearly more robust (see DONE.md v0.4.1).

const (
	defaultIdentifyThreshold = 0.55  // min similarity turn <-> person voiceprint
	identifyOutsiderBonus    = 0.05  // people not on the podcast's roster need this much more
	identifyMargin           = 0.03  // best person must beat the second best by this much
	identifyMinTurnMs        = 1500  // shorter turns are too unreliable on their own
	identifyMaxTurnMs        = 20000 // longer turns: the first 20 s are enough
	sampleMinMs              = 2000  // shortest passage that becomes a voice sample
	sampleMaxMs              = 30000
)

func identifyThreshold(st *Store) float64 {
	if v, err := strconv.ParseFloat(st.Setting("identify_threshold", ""), 64); err == nil && v > 0 && v < 1 {
		return v
	}
	return defaultIdentifyThreshold
}

// decodeAudio returns 16 kHz mono samples of a file (or a part of it) via ffmpeg.
func decodeAudio(ctx context.Context, file string, startMs, endMs int64) ([]float32, error) {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if startMs > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", float64(startMs)/1000))
	}
	if endMs > startMs {
		args = append(args, "-t", fmt.Sprintf("%.3f", float64(endMs-startMs)/1000))
	}
	args = append(args, "-i", file, "-ar", "16000", "-ac", "1", "-f", "s16le", "-")
	cmd := exec.CommandContext(ctx, ffmpegPath(), args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	raw, rerr := io.ReadAll(out)
	werr := cmd.Wait()
	if rerr != nil {
		return nil, rerr
	}
	if werr != nil {
		return nil, fmt.Errorf("ffmpeg: %v %s", werr, strings.TrimSpace(stderr.String()))
	}
	s := make([]float32, len(raw)/2)
	for i := range s {
		s[i] = float32(int16(binary.LittleEndian.Uint16(raw[2*i:]))) / 32768
	}
	return s, nil
}

type embedder struct {
	ex    *sherpa.SpeakerEmbeddingExtractor
	guard func()
}

func newEmbedder() (*embedder, error) {
	prov := providerNow()
	guard := gpuGuard(prov)
	ex := sherpa.NewSpeakerEmbeddingExtractor(&sherpa.SpeakerEmbeddingExtractorConfig{
		Model: embeddingModelPath(), NumThreads: sherpaThreads(), Provider: prov,
	})
	if ex == nil {
		guard()
		return nil, fmt.Errorf("could not load voiceprint model")
	}
	return &embedder{ex, guard}, nil
}

func (e *embedder) close() { sherpa.DeleteSpeakerEmbeddingExtractor(e.ex); e.guard() }

func (e *embedder) embed(samples []float32) []float32 {
	if len(samples) < sampleRate/2 {
		return nil
	}
	st := e.ex.CreateStream()
	defer sherpa.DeleteOnlineStream(st)
	st.AcceptWaveform(sampleRate, samples)
	st.InputFinished()
	if !e.ex.IsReady(st) {
		return nil
	}
	return e.ex.Compute(st)
}

// ------------------------------------------------------------ voice samples

// addSampleFromRange stores a confirmed voice sample for a person from a
// passage of a version's saved audio.
func addSampleFromRange(ctx context.Context, st *Store, v Version, personID, correctionID, startMs, endMs int64) error {
	if endMs-startMs < sampleMinMs || v.AudioFile == "" {
		return nil
	}
	if corr, _ := st.Corrections(v.ID); markedShare(markedIntervals(corr, markClip), startMs, endMs) >= 0.5 {
		return nil // movie clip: actors must not train anyone's voiceprint
	}
	if endMs-startMs > sampleMaxMs {
		endMs = startMs + sampleMaxMs
	}
	samples, err := decodeAudio(ctx, filepath.Join(P.Audio, v.AudioFile), startMs, endMs)
	if err != nil {
		return err
	}
	em, err := newEmbedder()
	if err != nil {
		return err
	}
	defer em.close()
	vec := em.embed(samples)
	if vec == nil {
		return nil
	}
	return st.AddVoiceSample(VoiceSample{PersonID: personID, VersionID: v.ID, CorrectionID: correctionID,
		StartMs: startMs, EndMs: endMs, Seconds: float64(len(samples)) / sampleRate, Source: "confirmed", Vec: vec})
}

// addSampleFromCluster stores the voiceprint of a whole detected voice
// (named by the user) as a sample, pointing at its longest turn for playback.
func addSampleFromCluster(st *Store, v Version, personID, correctionID int64, cluster int) error {
	embs, err := st.ClusterEmbeddings(v.ID)
	if err != nil {
		return err
	}
	_, _, turns, _ := st.LoadResults(v.ID)
	if corr, _ := st.Corrections(v.ID); clusterClipShare(turns, markedIntervals(corr, markClip), cluster) >= 0.5 {
		return nil // a voice that mostly speaks in movie clips
	}
	var longest Turn
	for _, t := range turns {
		if t.Cluster == cluster && t.EndMs-t.StartMs > longest.EndMs-longest.StartMs {
			longest = t
		}
	}
	for _, e := range embs {
		if e.Cluster == cluster && e.Seconds*1000 >= sampleMinMs {
			return st.AddVoiceSample(VoiceSample{PersonID: personID, VersionID: v.ID, CorrectionID: correctionID,
				StartMs: longest.StartMs, EndMs: longest.EndMs, Seconds: e.Seconds, Source: "confirmed", Vec: e.Vec})
		}
	}
	return nil
}

// ------------------------------------------------------------ identification

type personRef struct {
	id       int64
	centroid []float64
	onRoster bool
}

func personRefs(st *Store, feedID, episodeID int64) ([]personRef, error) {
	samples, err := st.VoiceSamples(0)
	if err != nil {
		return nil, err
	}
	limited, inEpisode := st.EpisodePeople(episodeID)
	roster, _ := st.Roster(feedID)
	sum := map[int64][]float64{}
	for _, s := range samples {
		n := normalize(s.Vec)
		if sum[s.PersonID] == nil {
			sum[s.PersonID] = make([]float64, len(n))
		}
		for i := range n {
			sum[s.PersonID][i] += n[i]
		}
	}
	var refs []personRef
	for id, v := range sum {
		if limited && !inEpisode[id] {
			continue // the user said this person is not in the episode
		}
		_, on := roster[id]
		refs = append(refs, personRef{id: id, centroid: v, onRoster: on})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].id < refs[j].id })
	return refs, nil
}

type identifyResult struct {
	Ranges      int
	IdentMs     int64
	SpeechMs    int64
	PerPerson   map[int64]int64
	PeopleKnown int
}

// identifyVersion names speaker turns of a version by comparing them with the
// known people's voiceprints, and stores the result as automatic corrections
// (replacing earlier automatic identification). samples may be nil: then the
// version's saved audio is decoded.
func identifyVersion(ctx context.Context, st *Store, v Version, samples []float32) (identifyResult, error) {
	res := identifyResult{PerPerson: map[int64]int64{}}
	ep, err := st.Episode(v.EpisodeID)
	if err != nil {
		return res, err
	}
	refs, err := personRefs(st, ep.FeedID, ep.ID)
	if err != nil {
		return res, err
	}
	res.PeopleKnown = len(refs)
	if err := st.DeleteAutoCorrectionsKind(v.ID, "range"); err != nil {
		return res, err
	}
	if len(refs) == 0 {
		return res, nil
	}
	_, _, turns, err := st.LoadResults(v.ID)
	if err != nil {
		return res, err
	}
	corr, _ := st.Corrections(v.ID)
	manual := map[int]bool{} // voices the user named / merged himself: keep his decision
	for _, c := range corr {
		if c.Kind == "merge" && !c.Auto {
			manual[c.From] = true
		}
	}
	if samples == nil {
		if v.AudioFile == "" {
			return res, fmt.Errorf("this version has no saved audio")
		}
		setStage("Loading audio", -1)
		samples, err = decodeAudio(ctx, filepath.Join(P.Audio, v.AudioFile), 0, 0)
		if err != nil {
			return res, err
		}
	}
	em, err := newEmbedder()
	if err != nil {
		return res, err
	}
	defer em.close()

	th := identifyThreshold(st)
	overlaps := overlapRegions(turns)
	clips := markedIntervals(corr, markClip) // actors in movie clips are not the hosts
	assigned := make([]int64, len(turns))    // person id per turn, 0 = none
	for i, t := range turns {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if i%20 == 0 {
			setStage("Identifying speakers", i*100/max(len(turns), 1))
		}
		d := t.EndMs - t.StartMs
		res.SpeechMs += d
		if manual[t.Cluster] || d < identifyMinTurnMs || overlapWith(overlaps, t.StartMs, t.EndMs)*2 > d ||
			markedShare(clips, t.StartMs, t.EndMs) >= 0.5 {
			continue
		}
		end := t.EndMs
		if d > identifyMaxTurnMs {
			end = t.StartMs + identifyMaxTurnMs
		}
		a, b := int(t.StartMs*sampleRate/1000), int(end*sampleRate/1000)
		if a < 0 || b > len(samples) || b <= a {
			continue
		}
		vec := em.embed(samples[a:b])
		if vec == nil {
			continue
		}
		n := normalize(vec)
		best, second, bestID := -2.0, -2.0, int64(0)
		bestRoster := false
		for _, r := range refs {
			s := cosine(n, r.centroid)
			if s > best {
				second, best, bestID, bestRoster = best, s, r.id, r.onRoster
			} else if s > second {
				second = s
			}
		}
		need := th
		if !bestRoster {
			need += identifyOutsiderBonus
		}
		if best >= need && best-second >= identifyMargin {
			assigned[i] = bestID
		}
	}

	// short / uncertain turns of a voice follow the voice's clear majority
	clusterTime := map[int]map[int64]int64{}
	for i, t := range turns {
		if assigned[i] != 0 {
			if clusterTime[t.Cluster] == nil {
				clusterTime[t.Cluster] = map[int64]int64{}
			}
			clusterTime[t.Cluster][assigned[i]] += t.EndMs - t.StartMs
		}
	}
	majority := map[int]int64{}
	for c, m := range clusterTime {
		var total, bestV int64
		var bestP int64
		for p, v := range m {
			total += v
			if v > bestV {
				bestP, bestV = p, v
			}
		}
		if bestV >= 5000 && float64(bestV) >= 0.7*float64(total) {
			majority[c] = bestP
		}
	}
	for i, t := range turns {
		if assigned[i] == 0 && !manual[t.Cluster] && overlapWith(overlaps, t.StartMs, t.EndMs)*2 <= t.EndMs-t.StartMs &&
			markedShare(clips, t.StartMs, t.EndMs) < 0.5 {
			assigned[i] = majority[t.Cluster]
		}
	}

	// store as ranges; join neighbouring turns of the same person
	type rng struct {
		a, b int64
		p    int64
	}
	var rs []rng
	for i, t := range turns {
		p := assigned[i]
		if p == 0 {
			continue
		}
		res.IdentMs += t.EndMs - t.StartMs
		res.PerPerson[p] += t.EndMs - t.StartMs
		if n := len(rs); n > 0 && rs[n-1].p == p && t.StartMs-rs[n-1].b < 1000 {
			rs[n-1].b = max64(rs[n-1].b, t.EndMs)
			continue
		}
		rs = append(rs, rng{t.StartMs, t.EndMs, p})
	}
	for _, r := range rs {
		if _, err := st.AddCorrectionID(v.ID, Correction{Kind: "range", StartMs: r.a, EndMs: r.b, Label: personLabel(r.p), Auto: true}); err != nil {
			return res, err
		}
	}
	res.Ranges = len(rs)
	return res, nil
}

func (r identifyResult) String() string {
	if r.PeopleKnown == 0 {
		return "no known voices yet"
	}
	pct := 0
	if r.SpeechMs > 0 {
		pct = int(r.IdentMs * 100 / r.SpeechMs)
	}
	type pp struct {
		id int64
		ms int64
	}
	var ps []pp
	for id, ms := range r.PerPerson {
		ps = append(ps, pp{id, ms})
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].ms > ps[j].ms })
	var parts []string
	for _, p := range ps {
		parts = append(parts, fmt.Sprintf("%s %s", personName(p.id), (time.Duration(p.ms)*time.Millisecond).Round(time.Second)))
	}
	return fmt.Sprintf("%d%% of speech identified (%s)", pct, strings.Join(parts, ", "))
}

// runIdentify is the pipeline hook: only runs when voices are known.
func runIdentify(ctx context.Context, st *Store, v *Version, samples []float32) {
	res, err := identifyVersion(ctx, st, *v, samples)
	if err != nil {
		logf("   warning: speaker identification failed: %v", err)
		return
	}
	if res.PeopleKnown > 0 {
		logf("   identified: %s", res)
	}
}
