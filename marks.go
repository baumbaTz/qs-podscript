package main

import "sort"

// Marks: stretches of an episode tagged as an ad or a movie clip. They are
// independent of who speaks (a host reading an ad is still the host) and are
// stored as corrections of kind "mark" with Text = the mark ("" clears an
// earlier mark in that range; later marks win, like speaker corrections).
//
// What marks change:
//   - the transcript shows marked paragraphs tinted with a badge and can hide them
//   - the text download can leave ads out
//   - clip passages never become voice samples, and recognition skips turns
//     inside clips (actor voices must not blur the hosts' voiceprints)
//   - they are carried over to new versions like text corrections

const (
	markAd   = "ad"
	markClip = "clip"
)

func validMark(m string) bool { return m == "" || m == markAd || m == markClip }

func markName(m string) string {
	switch m {
	case markAd:
		return "Ad"
	case markClip:
		return "Movie clip"
	}
	return "Normal"
}

func markCorrections(corr []Correction) []Correction {
	var out []Correction
	for _, c := range corr {
		if c.Kind == "mark" {
			out = append(out, c)
		}
	}
	return out
}

// markAt returns the mark at a moment (later marks win).
func markAt(marks []Correction, ms int64) string {
	m := ""
	for _, c := range marks {
		if ms >= c.StartMs && ms < c.EndMs {
			m = c.Text
		}
	}
	return m
}

// markedIntervals flattens the marks into non-overlapping intervals of one kind.
func markedIntervals(corr []Correction, kind string) []interval {
	marks := markCorrections(corr)
	if len(marks) == 0 {
		return nil
	}
	// every boundary is a point where the mark may change
	var pts []int64
	for _, c := range marks {
		pts = append(pts, c.StartMs, c.EndMs)
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i] < pts[j] })
	var out []interval
	for i := 0; i+1 < len(pts); i++ {
		a, b := pts[i], pts[i+1]
		if b <= a || markAt(marks, a) != kind {
			continue
		}
		if n := len(out); n > 0 && out[n-1].b == a {
			out[n-1].b = b
			continue
		}
		out = append(out, interval{a, b})
	}
	return out
}

// markedShare: which fraction of [a,b] lies inside marks of that kind.
func markedShare(ivs []interval, a, b int64) float64 {
	if b <= a {
		return 0
	}
	return float64(overlapWith(ivs, a, b)) / float64(b-a)
}

// applyMarks tags every word with its mark and splits paragraphs where the
// mark changes.
func applyMarks(us []Utterance, corr []Correction) []Utterance {
	marks := markCorrections(corr)
	if len(marks) == 0 {
		return us
	}
	var out []Utterance
	for _, u := range us {
		var cur *Utterance
		for _, w := range u.Words {
			m := markAt(marks, (w.StartMs+w.EndMs)/2)
			if cur == nil || cur.Mark != m {
				out = append(out, Utterance{StartMs: w.StartMs, Label: u.Label, Mark: m})
				cur = &out[len(out)-1]
			}
			cur.Words = append(cur.Words, w)
			cur.EndMs = max64(cur.EndMs, w.EndMs)
		}
		if cur != nil {
			cur.EndMs = max64(cur.EndMs, u.EndMs)
		}
	}
	return out
}

// markLane: where ads / clips are, for the overview above the transcript.
type markLane struct {
	Mark    string
	Name    string
	TotalMs int64
	Blocks  []laneBlock
}

func buildMarkLanes(us []Utterance, totalMs int64) []markLane {
	if totalMs <= 0 && len(us) > 0 {
		totalMs = us[len(us)-1].EndMs
	}
	if totalMs <= 0 {
		return nil
	}
	var out []markLane
	for _, m := range []string{markAd, markClip} {
		l := markLane{Mark: m, Name: markName(m) + "s"}
		for _, u := range us {
			if u.Mark != m {
				continue
			}
			d := max64(u.EndMs-u.StartMs, 0)
			l.TotalMs += d
			l.Blocks = append(l.Blocks, laneBlock{
				Left:  float64(u.StartMs) * 100 / float64(totalMs),
				Width: max(float64(d)*100/float64(totalMs), 0.15),
			})
		}
		if len(l.Blocks) > 0 {
			out = append(out, l)
		}
	}
	return out
}

// clusterClipShare: which fraction of a voice's speaking time is inside clips.
func clusterClipShare(turns []Turn, clips []interval, cluster int) float64 {
	var total, in int64
	for _, t := range turns {
		if t.Cluster == cluster {
			total += t.EndMs - t.StartMs
			in += overlapWith(clips, t.StartMs, t.EndMs)
		}
	}
	if total == 0 {
		return 0
	}
	return float64(in) / float64(total)
}
