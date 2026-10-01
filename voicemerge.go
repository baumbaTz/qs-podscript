package main

import (
	"fmt"
	"math"
	"sort"
)

// Second pass after speaker detection: sherpa is run with a "safe" threshold
// that rarely merges different people but often splits one person into
// several voices. Here we compare the per-voice voiceprints (ResNet34, up to
// 30 s of clean speech each) and merge voices that are clearly the same
// person. The result is stored as automatic merge corrections, so it is
// visible and can be undone like a manual correction.

const defaultVoiceMergeSimilarity = 0.0 // cosine similarity; 0 = off (see DONE.md: weak gains, risk of wrong merges)

type voiceMerge struct {
	From, Into int
	Similarity float64
}

func normalize(v []float32) []float64 {
	out := make([]float64, len(v))
	var n float64
	for i, x := range v {
		out[i] = float64(x)
		n += float64(x) * float64(x)
	}
	n = math.Sqrt(n)
	if n == 0 {
		return out
	}
	for i := range out {
		out[i] /= n
	}
	return out
}

func cosine(a, b []float64) float64 {
	var d, na, nb float64
	for i := range a {
		d += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return d / math.Sqrt(na*nb)
}

// planVoiceMerges clusters the voices of one episode agglomeratively: always
// merge the most similar pair first, as long as it is above minSim. The voice
// with more speaking time survives; the merged voiceprint is the time-weighted
// average. talkMs = speaking time per cluster.
func planVoiceMerges(embs []ClusterEmbedding, talkMs map[int]int64, minSim float64) []voiceMerge {
	type group struct {
		id   int
		vec  []float64
		w    float64 // seconds of audio behind the voiceprint
		talk int64
	}
	var gs []*group
	for _, e := range embs {
		if len(e.Vec) == 0 {
			continue
		}
		gs = append(gs, &group{id: e.Cluster, vec: normalize(e.Vec), w: math.Max(e.Seconds, 0.5), talk: talkMs[e.Cluster]})
	}
	var merges []voiceMerge
	for len(gs) > 1 {
		bi, bj, best := -1, -1, -2.0
		for i := 0; i < len(gs); i++ {
			for j := i + 1; j < len(gs); j++ {
				if s := cosine(gs[i].vec, gs[j].vec); s > best {
					bi, bj, best = i, j, s
				}
			}
		}
		if best < minSim {
			break
		}
		a, b := gs[bi], gs[bj]
		keep, gone := a, b
		if b.talk > a.talk || (b.talk == a.talk && b.id < a.id) {
			keep, gone = b, a
		}
		merges = append(merges, voiceMerge{From: gone.id, Into: keep.id, Similarity: best})
		for k := range keep.vec {
			keep.vec[k] = (keep.vec[k]*keep.w + gone.vec[k]*gone.w) / (keep.w + gone.w)
		}
		keep.w += gone.w
		keep.talk += gone.talk
		// remove "gone"
		idx := bj
		if gone == a {
			idx = bi
		}
		gs = append(gs[:idx], gs[idx+1:]...)
	}
	return merges
}

// similarityReport lists the most similar voice pairs (for the log): helps to
// see how close the merge threshold is to the actual numbers.
func similarityReport(embs []ClusterEmbedding, n int) string {
	type pair struct {
		a, b int
		s    float64
	}
	var ps []pair
	for i := 0; i < len(embs); i++ {
		for j := i + 1; j < len(embs); j++ {
			ps = append(ps, pair{embs[i].Cluster, embs[j].Cluster, cosine(normalize(embs[i].Vec), normalize(embs[j].Vec))})
		}
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].s > ps[j].s })
	out := ""
	for i, p := range ps {
		if i >= n {
			break
		}
		out += fmt.Sprintf(" %d+%d=%.2f", p.a+1, p.b+1, p.s)
	}
	return out
}

// applyVoiceMerge runs the pass for a stored version: replaces earlier
// automatic merges, keeps manual corrections. Returns number of merges.
func applyVoiceMerge(st *Store, versionID int64, minSim float64) (int, error) {
	if err := st.DeleteAutoCorrectionsKind(versionID, "merge"); err != nil {
		return 0, err
	}
	if minSim <= 0 {
		return 0, nil
	}
	embs, err := st.ClusterEmbeddings(versionID)
	if err != nil {
		return 0, err
	}
	_, _, turns, err := st.LoadResults(versionID)
	if err != nil {
		return 0, err
	}
	talk := map[int]int64{}
	for _, t := range turns {
		talk[t.Cluster] += t.EndMs - t.StartMs
	}
	debugf("voice similarity v%d (top pairs, speaker numbers):%s", versionID, similarityReport(embs, 12))
	merges := planVoiceMerges(embs, talk, minSim)
	for _, m := range merges {
		if err := st.AddCorrection(versionID, Correction{Kind: "merge", From: m.From, Label: m.Into, Auto: true, Score: m.Similarity}); err != nil {
			return 0, err
		}
	}
	return len(merges), nil
}
