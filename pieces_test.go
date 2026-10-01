package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestPlanChunksCoversEverything(t *testing.T) {
	// 10 min: speech with pauses, a 70 s stretch without detected speech
	// (a song), then speech again
	var turns []Turn
	for ms := int64(0); ms < 300000; ms += 7000 {
		turns = append(turns, Turn{ms, ms + 6000, int(ms/7000) % 3}) // 1 s pauses
	}
	for ms := int64(370000); ms < 600000; ms += 12000 {
		turns = append(turns, Turn{ms, ms + 12000, 1}) // no pauses, speaker changes only
	}
	ps := planChunks(turns, 600000)
	if ps[0].a != 0 || ps[len(ps)-1].b != 600000 {
		t.Fatalf("not covering: %v .. %v", ps[0], ps[len(ps)-1])
	}
	for i, p := range ps {
		if p.b-p.a > chunkMaxMs {
			t.Fatalf("piece %d too long: %d ms", i, p.b-p.a)
		}
		if i > 0 && ps[i-1].b != p.a {
			t.Fatalf("gap or overlap between pieces %d and %d", i-1, i)
		}
	}
	// in the part with pauses every cut must be inside a pause
	for _, p := range ps {
		if p.b < 300000 && p.b%7000 != 6500 {
			t.Fatalf("cut at %d is not in the middle of a pause", p.b)
		}
	}
}

func words(text string, start, step int64) []Token {
	var ts []Token
	for i, w := range strings.Fields(text) {
		ts = append(ts, Token{SegIdx: 0, Idx: i, StartMs: start + int64(i)*step, EndMs: start + int64(i+1)*step, Text: " " + w, P: 0.9})
	}
	return ts
}

func TestTrimLoops(t *testing.T) {
	loop := "ie Lannister. I don't care that you did your brother anyway." + strings.Repeat(" I don't care about that.", 14) + " So anyway."
	toks := words(loop, 0, 300)
	segs := []Segment{{Idx: 0, StartMs: 0, EndMs: 30000, Text: loop}}
	s, tk, n := trimLoops(segs, toks)
	if n != 1 {
		t.Fatalf("loops trimmed: %d", n)
	}
	want := "ie Lannister. I don't care that you did your brother anyway. I don't care about that. I don't care about that. [...] So anyway."
	if s[0].Text != want {
		t.Fatalf("\n got: %s\nwant: %s", s[0].Text, want)
	}
	var marker Token
	for _, x := range tk {
		if x.Text == " [...]" {
			marker = x
		}
	}
	if marker.P != 0 || marker.EndMs <= marker.StartMs {
		t.Fatalf("marker %+v", marker)
	}

	// hosts repeating a phrase a few times is normal speech - untouched
	human := "Get to the chopper! Get to the chopper! Get to the chopper! Get to the chopper! he yells"
	s2, _, n2 := trimLoops([]Segment{{Idx: 0, Text: human}}, words(human, 0, 300))
	if n2 != 0 || s2[0].Text != human {
		t.Fatalf("human repetition changed: %d %q", n2, s2[0].Text)
	}
	if chunkLoops([]Segment{{Text: human}}) {
		t.Fatal("4 repetitions counted as loop")
	}
	if !chunkLoops([]Segment{{Text: loop}}) {
		t.Fatal("loop not detected")
	}
}

func TestTextCorrections(t *testing.T) {
	turns := []Turn{{0, 10000, 0}}
	segs := []Segment{{0, 0, 10000, "x"}}
	toks := words("so the movie was really gud and then", 0, 1000)
	toks[5].P = 0.1 // "gud": whisper unsure
	us := buildUtterances(segs, toks, turns, nil)
	if !us[0].Words[5].Uncertain() || us[0].Words[4].Uncertain() {
		t.Fatalf("confidence not carried: %+v", us[0].Words)
	}
	corr := []Correction{
		{Kind: "text", StartMs: 5000, EndMs: 6000, Text: "good"},
		{Kind: "text", StartMs: 6000, EndMs: 8000, Text: ""}, // delete "and then"
	}
	us = buildUtterances(segs, toks, turns, corr)
	if got := us[0].Text; got != "so the movie was really good" {
		t.Fatalf("got %q", got)
	}
	if us[0].Words[5].Uncertain() {
		t.Fatal("corrected word still uncertain")
	}
}

func TestPlanCarry(t *testing.T) {
	scott, randy := personLabel(1), personLabel(2)
	// old version: voice 0 0-5 s, voice 1 6-9 s, voice 0 9-12 s, voice 2 13-15 s
	oldTurns := []Turn{{0, 5000, 0}, {6000, 9000, 1}, {9000, 12000, 0}, {13000, 15000, 2}}
	segs := []Segment{{0, 0, 15000, "x"}}
	var toks []Token
	for i := int64(0); i < 15; i++ {
		if i == 5 || i == 12 { // pauses
			continue
		}
		toks = append(toks, tok(0, int(i), i*1000+100, i*1000+900, fmt.Sprintf(" w%d", i)))
	}
	corr := []Correction{
		{Kind: "merge", From: 0, Label: scott},                                // user: voice 0 = Scott
		{Kind: "range", StartMs: 6000, EndMs: 9000, Label: randy, Auto: true}, // recognized, user left it
		{Kind: "text", StartMs: 1000, EndMs: 2000, Text: "Film Sack"},         // user text fix
	}
	got := planCarry(segs, toks, oldTurns, corr)
	// Scott 0-5 s and 9-12 s, Randy 6-9 s (recognized!) stretched into the
	// pauses, voice 2 (unnamed) not carried, then the text correction
	type want struct {
		label  int
		a, b   int64
		origin string
	}
	var have []want
	for _, c := range got {
		if c.Kind == "range" {
			have = append(have, want{c.Label, c.StartMs, c.EndMs, c.Origin})
		}
	}
	if len(have) != 3 || have[0].label != scott || have[1].label != randy || have[2].label != scott {
		t.Fatalf("ranges %+v", have)
	}
	if have[1].a > 6100 || have[0].b < 4900 || have[1].origin != originCarried {
		t.Fatalf("not stretched into the pause / origin: %+v", have)
	}
	if have[2].b > 13000+100 { // must not grow into the unnamed voice's words
		t.Fatalf("grew into next passage: %+v", have[2])
	}
	last := got[len(got)-1]
	if last.Kind != "text" || last.Text != "Film Sack" || last.Origin != "" {
		t.Fatalf("text correction: %+v", last)
	}
}

func TestVoicesCoveredBy(t *testing.T) {
	scott := personLabel(1)
	turns := []Turn{{0, 5000, 0}, {9000, 12000, 0}, {5000, 9000, 1}}
	plan := []Correction{
		{Kind: "range", StartMs: 0, EndMs: 5000, Label: scott},
		{Kind: "range", StartMs: 9000, EndMs: 12000, Label: scott},
		{Kind: "range", StartMs: 5000, EndMs: 6000, Label: personLabel(2)}, // only 25 % of voice 1
	}
	ms := voicesCoveredBy(turns, plan)
	if len(ms) != 1 || ms[0].From != 0 || ms[0].Label != scott {
		t.Fatalf("%+v", ms)
	}
}

func TestPlanChunksNoTinyLastPiece(t *testing.T) {
	for _, total := range []int64{28001, 28500, 36000, 56010, 60000, 100} {
		ps := planChunks(nil, total)
		if ps[len(ps)-1].b != total {
			t.Fatalf("%d: not covered", total)
		}
		for _, p := range ps {
			if len(ps) > 1 && p.b-p.a < chunkMinMs {
				t.Fatalf("total %d: tiny piece %v in %v", total, p, ps)
			}
			if p.b-p.a > chunkMaxMs {
				t.Fatalf("total %d: piece too long %v", total, p)
			}
		}
	}
}

func TestPeriodStaysWithItsWord(t *testing.T) {
	scott, randy := personLabel(1), personLabel(2)
	turns := []Turn{{0, 1400, 0}, {1450, 4000, 1}}
	segs := []Segment{{0, 0, 4000, "x"}}
	toks := []Token{
		tok(0, 0, 0, 500, " I"), tok(0, 1, 500, 900, " don't"), tok(0, 2, 900, 1300, " care"),
		tok(0, 3, 1500, 1600, "."), // whisper puts the period into the next turn
		tok(0, 4, 1700, 2200, " Well"), tok(0, 5, 2200, 2300, ","), tok(0, 6, 2400, 3000, " okay"),
	}
	corr := []Correction{ // as after carrying corrections over
		{Kind: "range", StartMs: 0, EndMs: 1400, Label: scott},
		{Kind: "range", StartMs: 1450, EndMs: 4000, Label: randy},
	}
	peopleMu.Lock()
	peopleNames = map[int64]string{1: "Scott", 2: "Randy"}
	peopleMu.Unlock()
	got := linesOf(buildUtterances(segs, toks, turns, corr))
	if got != "Scott:I don't care. | Randy:Well, okay" {
		t.Fatalf("got %s", got)
	}
}

func TestRepetitionSuspicious(t *testing.T) {
	seg := func(s string) []Segment { return []Segment{{Text: s}} }
	cases := []struct {
		text string
		reps int
		susp bool
	}{
		{"so I watched the movie and it was fine", 1, false},
		{"Get to the chopper! Get to the chopper! Get to the chopper!", 3, false}, // hosts quoting
		{strings.Repeat("I don't care about that. ", 4) + "anyway", 4, true},      // hiccup: retry
		{strings.Repeat("you know ", 6) + "right", 6, true},                       // 2-word stutter loop
		{"no no no no no", 1, false},                                              // single words: normal speech
	}
	for _, c := range cases {
		if got := maxRepeats(seg(c.text)); got != c.reps {
			t.Errorf("%q: reps %d want %d", c.text, got, c.reps)
		}
		if got := repetitionSuspicious(seg(c.text)); got != c.susp {
			t.Errorf("%q: suspicious %v", c.text, got)
		}
	}
}
