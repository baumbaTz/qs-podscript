package main

import "sort"

// Carrying a checked transcript over to a new version of the same audio.
//
// When the user has corrected a version, "Transcribe again" / "Redo speaker
// detection" must reproduce what that version SHOWED - including passages
// the automatic recognition got right and the user therefore left alone.
// Speaker numbers ("Speaker 7") differ between versions, but people and time
// don't: the displayed timeline of people, crosstalk and unknown is copied as
// time ranges (origin "carried"), stretched into the gaps to the neighbouring
// passages so slightly different word timing in the new transcript still
// lands inside. Text corrections are copied as they are. Unnamed voices
// can't be carried.

const (
	originCarried = "carried"
	carryStretch  = 700 // ms a carried passage may grow into a gap on each side
)

func manualCorrections(corr []Correction) []Correction {
	var out []Correction
	for _, c := range corr {
		if !c.Auto {
			out = append(out, c)
		}
	}
	return out
}

func carryable(label int) bool {
	_, isPerson := labelPerson(label)
	return isPerson || label < 0
}

// planCarry turns what the old version displays into corrections for a new
// version: speaker passages first (lowest priority), then text corrections.
func planCarry(segs []Segment, toks []Token, turns []Turn, corr []Correction) []Correction {
	shown := buildUtterances(segs, toks, turns, corr)

	// runs of consecutive words with the same label, across paragraphs
	type run struct {
		a, b  int64
		label int
	}
	var runs []run
	for _, u := range shown {
		for _, w := range u.Words {
			if n := len(runs); n > 0 && runs[n-1].label == u.Label && w.StartMs-runs[n-1].b < 2000 {
				runs[n-1].b = max64(runs[n-1].b, w.EndMs)
				continue
			}
			runs = append(runs, run{w.StartMs, max64(w.EndMs, w.StartMs+1), u.Label})
		}
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].a < runs[j].a })

	var out []Correction
	for i, r := range runs {
		if !carryable(r.label) {
			continue
		}
		a, b := r.a, r.b
		// stretch into the gaps, at most halfway to the neighbours
		if i > 0 {
			if gap := r.a - runs[i-1].b; gap > 0 {
				a -= min64(carryStretch, gap/2)
			}
		} else {
			a -= min64(carryStretch, r.a)
		}
		if i+1 < len(runs) {
			if gap := runs[i+1].a - r.b; gap > 0 {
				b += min64(carryStretch, gap/2)
			}
		} else {
			b += carryStretch
		}
		if n := len(out); n > 0 && out[n-1].Label == r.label && a-out[n-1].EndMs <= 0 {
			out[n-1].EndMs = b
			continue
		}
		out = append(out, Correction{Kind: "range", StartMs: a, EndMs: b, Label: r.label, Origin: originCarried})
	}
	for _, c := range manualCorrections(corr) {
		if c.Kind == "text" || c.Kind == "mark" {
			c.Origin = ""
			out = append(out, c)
		}
	}
	return out
}

// carryCorrections copies the checked state of version from to version to.
func carryCorrections(st *Store, from, to Version) (int, error) {
	corr, err := st.Corrections(from.ID)
	if err != nil || (len(manualCorrections(corr)) == 0 && !hasChecks(st, from.ID)) {
		return 0, err
	}
	segs, toks, oldTurns, err := st.LoadResults(from.ID)
	if err != nil {
		return 0, err
	}
	plan := planCarry(segs, toks, oldTurns, corr)
	// passages someone confirmed in "Look Who's Talking" stay confirmed
	if nc, err := carryChecks(st, from, to, buildUtterances(segs, toks, oldTurns, corr)); err != nil {
		logf("   warning: could not carry checked passages over: %v", err)
	} else if nc > 0 {
		logf("   carried over %d checked passages", nc)
	}
	n := 0
	for _, c := range plan {
		c.ID, c.Auto = 0, false
		if _, err := st.AddCorrectionID(to.ID, c); err != nil {
			return n, err
		}
		n++
	}
	// voices of the new version that (almost) entirely lie in one person's
	// carried passages become that person as a whole, so words at turn edges
	// follow too
	_, _, newTurns, err := st.LoadResults(to.ID)
	if err != nil {
		return n, err
	}
	for _, m := range voicesCoveredBy(newTurns, plan) {
		m.Origin = originCarried
		if _, err := st.AddCorrectionID(to.ID, m); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// voicesCoveredBy: merge corrections for voices whose speaking time is at
// least 80 % inside carried passages of one single person.
func voicesCoveredBy(turns []Turn, plan []Correction) []Correction {
	total := map[int]int64{}
	byPerson := map[int]map[int]int64{}
	for _, t := range turns {
		total[t.Cluster] += t.EndMs - t.StartMs
		for _, c := range plan {
			if c.Kind != "range" {
				continue
			}
			if _, ok := labelPerson(c.Label); !ok {
				continue
			}
			if lo, hi := max64(t.StartMs, c.StartMs), min64(t.EndMs, c.EndMs); hi > lo {
				if byPerson[t.Cluster] == nil {
					byPerson[t.Cluster] = map[int]int64{}
				}
				byPerson[t.Cluster][c.Label] += hi - lo
			}
		}
	}
	var out []Correction
	clusters := make([]int, 0, len(total))
	for c := range total {
		clusters = append(clusters, c)
	}
	sort.Ints(clusters)
	for _, cl := range clusters {
		for person, ms := range byPerson[cl] {
			if total[cl] > 0 && float64(ms) >= 0.8*float64(total[cl]) {
				out = append(out, Correction{Kind: "merge", From: cl, Label: person})
			}
		}
	}
	return out
}

// hasManualCorrections: did a human work on this version (corrections or
// checked passages)? Then a new version of the same audio carries that over.
func hasManualCorrections(st *Store, versionID int64) bool {
	corr, err := st.Corrections(versionID)
	return err == nil && (len(manualCorrections(corr)) > 0 || hasChecks(st, versionID))
}
