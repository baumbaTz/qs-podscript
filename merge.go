package main

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Special speaker labels (real clusters are >= 0).
const (
	labelUnknown   = -1 // no speaker detected (music, ads, silence)
	labelCrosstalk = -2 // two or more people talking at the same time
)

type Word struct {
	Text    string
	StartMs int64
	EndMs   int64
	P       float64 // whisper's confidence (lowest token probability), 1 = sure
	Orig    string  // set when a spelling fix replaced what whisper wrote
	Hit     bool    // matches the search the page was opened from (?hl=)
}

// uncertainBelow: words below this confidence are shown as uncertain.
const uncertainBelow = 0.35

func (w Word) Uncertain() bool { return w.P < uncertainBelow }

type Utterance struct {
	StartMs int64
	EndMs   int64
	Label   int
	Text    string
	Words   []Word
	Mark    string // "" | "ad" | "clip" (see marks.go)
}

type interval struct{ a, b int64 }

// overlapRegions returns the time ranges where 2+ different clusters talk.
func overlapRegions(turns []Turn) []interval {
	type ev struct {
		t     int64
		delta int
		c     int
	}
	evs := make([]ev, 0, 2*len(turns))
	for _, t := range turns {
		if t.EndMs <= t.StartMs {
			continue
		}
		evs = append(evs, ev{t.StartMs, +1, t.Cluster}, ev{t.EndMs, -1, t.Cluster})
	}
	sort.Slice(evs, func(i, j int) bool {
		if evs[i].t != evs[j].t {
			return evs[i].t < evs[j].t
		}
		return evs[i].delta < evs[j].delta // ends before starts
	})
	active := map[int]int{}
	var out []interval
	var openAt int64 = -1
	for _, e := range evs {
		active[e.c] += e.delta
		if active[e.c] <= 0 {
			delete(active, e.c)
		}
		if len(active) >= 2 && openAt < 0 {
			openAt = e.t
		} else if len(active) < 2 && openAt >= 0 {
			if e.t > openAt {
				out = append(out, interval{openAt, e.t})
			}
			openAt = -1
		}
	}
	return out
}

func overlapWith(regions []interval, a, b int64) int64 {
	var sum int64
	for _, r := range regions {
		if r.a >= b {
			break
		}
		lo, hi := max64(a, r.a), min64(b, r.b)
		if hi > lo {
			sum += hi - lo
		}
	}
	return sum
}

// labelFor decides who spoke during [a,b].
func labelFor(turns []Turn, overlaps []interval, a, b int64) int {
	dur := b - a
	if dur <= 0 {
		dur = 1
		b = a + 1
	}
	if overlapWith(overlaps, a, b)*2 >= dur {
		return labelCrosstalk
	}
	best, bestOv := labelUnknown, int64(0)
	perCluster := map[int]int64{}
	for _, t := range turns {
		if t.StartMs >= b {
			break
		}
		lo, hi := max64(a, t.StartMs), min64(b, t.EndMs)
		if hi > lo {
			perCluster[t.Cluster] += hi - lo
			if perCluster[t.Cluster] > bestOv {
				best, bestOv = t.Cluster, perCluster[t.Cluster]
			}
		}
	}
	return best
}

// buildUtterances turns raw results into readable speaker-attributed lines.
// Whisper segments don't respect speaker changes, so we label each token by
// time and split a segment when the speaker changes inside it. Manual
// corrections are applied on top: speaker merges first, then time ranges
// (later corrections win).
func buildUtterances(segs []Segment, toks []Token, turns []Turn, corr []Correction) []Utterance {
	overlaps := overlapRegions(turns)
	merge := mergeMap(corr)
	var ranges []Correction
	for _, c := range corr {
		if c.Kind == "range" {
			ranges = append(ranges, c)
		}
	}
	mapLabel := func(l int) int {
		if l >= 0 {
			if to, ok := merge[l]; ok {
				return to
			}
		}
		return l
	}
	forcedLabel := func(a, b int64) (int, bool) {
		mid := (a + b) / 2
		label, ok := 0, false
		for _, r := range ranges {
			if mid >= r.StartMs && mid < r.EndMs {
				label, ok = mapLabel(r.Label), true // later ones win; merges apply too
			}
		}
		return label, ok
	}

	tokBySeg := map[int][]Token{}
	for _, t := range toks {
		tokBySeg[t.SegIdx] = append(tokBySeg[t.SegIdx], t)
	}

	var out []Utterance
	emit := func(label int, words []Word) {
		if len(words) == 0 {
			return
		}
		u := Utterance{StartMs: words[0].StartMs, EndMs: words[len(words)-1].EndMs, Label: label, Words: words}
		if n := len(out); n > 0 && out[n-1].Label == label && u.StartMs-out[n-1].EndMs < 2000 {
			out[n-1].Words = append(out[n-1].Words, words...)
			out[n-1].EndMs = u.EndMs
			return
		}
		out = append(out, u)
	}

	for _, seg := range segs {
		ts := tokBySeg[seg.Idx]
		if len(ts) == 0 {
			text := strings.TrimSpace(seg.Text)
			if text == "" {
				continue
			}
			l := mapLabel(labelFor(turns, overlaps, seg.StartMs, seg.EndMs))
			if f, ok := forcedLabel(seg.StartMs, seg.EndMs); ok {
				l = f
			}
			emit(l, []Word{{Text: text, StartMs: seg.StartMs, EndMs: seg.EndMs, P: 1}})
			continue
		}
		// speakers are decided per WORD (punctuation belongs to its word), timed
		// by the word's letters only: whisper's timestamps for "." and "," are
		// unreliable and used to land in the next speaker's turn
		words, spans := tokensToWordSpans(ts)
		if len(words) == 0 {
			continue
		}
		labels := make([]int, len(words))
		forced := make([]bool, len(words))
		for i, sp := range spans {
			labels[i] = mapLabel(labelFor(turns, overlaps, sp.a, sp.b))
		}
		anyForced := false
		for i, sp := range spans {
			if f, ok := forcedLabel(sp.a, sp.b); ok {
				labels[i], forced[i], anyForced = f, true, true
			}
		}
		// fill words in small gaps between turns AFTER the corrections, so
		// they take the corrected neighbour (e.g. the person), not the raw voice
		fillUnknownExcept(labels, forced)

		// dominant label covers (almost) the whole segment -> one line
		if !anyForced {
			counts := map[int]int{}
			top, topN := labels[0], 0
			for _, l := range labels {
				counts[l]++
				if counts[l] > topN {
					top, topN = l, counts[l]
				}
			}
			if float64(topN) >= 0.8*float64(len(labels)) {
				emit(top, words)
				continue
			}
		}

		// otherwise split into runs of the same label
		type run struct {
			from, to, label int
			forced          bool
		}
		var runs []run
		for i, l := range labels {
			if n := len(runs); n > 0 && runs[n-1].label == l && runs[n-1].forced == forced[i] {
				runs[n-1].to = i
			} else {
				runs = append(runs, run{i, i, l, forced[i]})
			}
		}
		// absorb single automatic words into the previous run - word timing is
		// fuzzy. Manually assigned runs are never absorbed.
		var merged []run
		for k, r := range runs {
			nextForced := k+1 < len(runs) && runs[k+1].forced
			if n := len(merged); n > 0 {
				prev := &merged[n-1]
				if !r.forced && !prev.forced && !nextForced && r.to == r.from {
					prev.to = r.to
					continue
				}
				if prev.label == r.label {
					prev.to = r.to
					prev.forced = prev.forced || r.forced
					continue
				}
			}
			merged = append(merged, r)
		}
		for _, r := range merged {
			emit(r.label, words[r.from:r.to+1])
		}
	}
	out = applyTextCorrections(out, corr)
	out = applyMarks(out, corr)
	for i := range out {
		parts := make([]string, len(out[i].Words))
		for j, w := range out[i].Words {
			parts[j] = w.Text
		}
		out[i].Text = strings.Join(parts, " ")
	}
	return out
}

// applyTextCorrections replaces the words inside each text correction's time
// range by the corrected text (empty text deletes them). Later corrections
// win. The new text goes where the first replaced word was.
func applyTextCorrections(us []Utterance, corr []Correction) []Utterance {
	for _, c := range corr {
		if c.Kind != "text" {
			continue
		}
		placed := false
		for i := range us {
			var kept []Word
			for _, w := range us[i].Words {
				mid := (w.StartMs + w.EndMs) / 2
				if mid >= c.StartMs && mid < c.EndMs {
					if !placed && strings.TrimSpace(c.Text) != "" {
						kept = append(kept, Word{Text: strings.TrimSpace(c.Text), StartMs: w.StartMs, EndMs: c.EndMs, P: 1})
					}
					placed = true
					continue
				}
				kept = append(kept, w)
			}
			us[i].Words = kept
		}
	}
	var out []Utterance
	for _, u := range us {
		if len(u.Words) > 0 {
			u.StartMs = u.Words[0].StartMs
			u.EndMs = max64(u.EndMs, u.Words[len(u.Words)-1].EndMs)
			out = append(out, u)
		}
	}
	return out
}

// tokensToWordSpans is tokensToWords plus, per word, the time span of its
// letter/digit tokens (used to decide who said it). Same grouping rules.
func tokensToWordSpans(ts []Token) ([]Word, []span) {
	var words []Word
	var spans []span
	var buf strings.Builder
	var cur Word
	var sp span
	flush := func() {
		if t := strings.TrimSpace(buf.String()); t != "" {
			cur.Text = t
			words = append(words, cur)
			if sp.b <= sp.a { // no letters (e.g. "-" or "...") or no duration
				sp = span{cur.StartMs, max64(cur.EndMs, cur.StartMs+1)}
			}
			spans = append(spans, sp)
		}
		buf.Reset()
	}
	for i, t := range ts {
		if i == 0 || strings.HasPrefix(t.Text, " ") {
			flush()
			cur = Word{StartMs: t.StartMs, P: 1}
			sp = span{}
		}
		buf.WriteString(t.Text)
		cur.EndMs = t.EndMs
		if strings.IndexFunc(t.Text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 ||
			strings.Contains(t.Text, "[") {
			cur.P = min(cur.P, t.P)
			if sp.b == 0 && sp.a == 0 {
				sp = span{t.StartMs, t.EndMs}
			} else {
				sp.a, sp.b = min64(sp.a, t.StartMs), max64(sp.b, t.EndMs)
			}
		}
	}
	flush()
	return words, spans
}

// tokensToWords glues whisper tokens into words: a token starting with a
// space starts a new word, others (punctuation, word pieces) attach to the
// previous one. See tokensToWordSpans.
func tokensToWords(ts []Token) []Word {
	w, _ := tokensToWordSpans(ts)
	return w
}

// mergeMap resolves "cluster A is really B" corrections, following chains
// (A->B, B->C gives A->C) and ignoring cycles.
func mergeMap(corr []Correction) map[int]int {
	direct := map[int]int{}
	for _, c := range corr {
		if c.Kind == "merge" && c.From != c.Label {
			direct[c.From] = c.Label
		}
	}
	out := map[int]int{}
	for from := range direct {
		to, seen := from, map[int]bool{from: true}
		for {
			next, ok := direct[to]
			if !ok || seen[next] {
				break
			}
			seen[next] = true
			to = next
		}
		out[from] = to
	}
	return out
}

// fillUnknown gives tokens in tiny gaps between diarization turns the label
// of their neighbour (previous if possible, otherwise next).
// fillUnknownExcept is fillUnknown that leaves forced positions alone (a
// passage the user marked "Unknown" must stay unknown).
func fillUnknownExcept(labels []int, forced []bool) {
	tmp := append([]int(nil), labels...)
	for i := range tmp {
		if forced[i] && tmp[i] == labelUnknown {
			tmp[i] = labelUnknown - 100 // placeholder: not filled, not a source
		}
	}
	fillUnknown(tmp)
	for i := range labels {
		if !forced[i] {
			if tmp[i] == labelUnknown-100 {
				continue // neighbour was a forced "unknown": stay unknown
			}
			labels[i] = tmp[i]
		}
	}
}

func fillUnknown(labels []int) {
	known := false
	for _, l := range labels {
		if l != labelUnknown {
			known = true
			break
		}
	}
	if !known {
		return
	}
	for i := range labels {
		if labels[i] == labelUnknown && i > 0 {
			labels[i] = labels[i-1]
		}
	}
	for i := len(labels) - 2; i >= 0; i-- {
		if labels[i] == labelUnknown {
			labels[i] = labels[i+1]
		}
	}
}

func labelName(l int) string {
	if id, ok := labelPerson(l); ok {
		return personName(id)
	}
	switch l {
	case labelUnknown:
		return "Unknown"
	case labelCrosstalk:
		return "[crosstalk]"
	}
	return fmt.Sprintf("Speaker %d", l+1)
}

func fmtTime(ms int64) string {
	s := ms / 1000
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, (s/60)%60, s%60)
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
