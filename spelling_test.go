package main

import "testing"

func spellUtt(words ...string) []Utterance {
	var ws []Word
	for i, w := range words {
		ws = append(ws, Word{Text: w, StartMs: int64(i) * 1000, EndMs: int64(i)*1000 + 500, P: 0.9})
	}
	return []Utterance{{Words: ws}}
}

func TestSpellingReplacements(t *testing.T) {
	rules := []SpellingRule{
		{Correct: "FilmSack", Variants: splitVariants("film sack, filmsack")},
		{Correct: "Brian Dunaway", Variants: splitVariants("Brian Done away")},
	}
	rules = append(rules, nameRules([]Person{{Name: "Brian Dunaway"}, {Name: "Randy"}})...)
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"Welcome", "to", "Film", "Sack!"}, "Welcome to FilmSack!"},
		{[]string{"the", "filmsack", "crew"}, "the FilmSack crew"},
		{[]string{"\"Film-Sack,\"", "right?"}, "\"FilmSack,\" right?"},
		{[]string{"film", "sack's", "best"}, "FilmSack's best"},
		{[]string{"I'm", "Brian", "Done", "away."}, "I'm Brian Dunaway."},
		{[]string{"brian", "dunaway", "here"}, "Brian Dunaway here"},
		{[]string{"we've", "done", "away", "with", "it"}, "we've done away with it"}, // not alone
		{[]string{"a", "film", "about", "a", "sack"}, "a film about a sack"},         // not in a row
		{[]string{"filmsacks", "are", "fun"}, "filmsacks are fun"},                   // whole words only
		{[]string{"randy", "said"}, "randy said"},                                    // single names aren't automatic
	}
	for _, c := range cases {
		us := applySpelling(spellUtt(c.in...), rules)
		got := us[0].Text
		if got == "" { // unchanged utterances keep Text empty in this test
			for i, w := range us[0].Words {
				if i > 0 {
					got += " "
				}
				got += w.Text
			}
		}
		if got != c.want {
			t.Errorf("%q -> %q, want %q", c.in, got, c.want)
		}
	}
	// a text correction (one Word with several words) stays one Word
	tc := spellUtt("x", "Welcome to Film Sack", "y")
	tc = applySpelling(tc, rules)
	if tc[0].Text != "x Welcome to FilmSack y" || len(tc[0].Words) != 3 || tc[0].Words[1].Orig != "Welcome to Film Sack" {
		t.Fatalf("text correction: %+v", tc[0].Words)
	}
	// ... and a match doesn't reach across its edge
	tc = applySpelling(spellUtt("Film", "Sack is great"), rules)
	if tc[0].Text != "" && tc[0].Text != "Film Sack is great" {
		t.Fatalf("across edge: %q", tc[0].Text)
	}
	us := applySpelling(spellUtt("Film", "Sack", "is"), rules)
	w := us[0].Words[0]
	if w.Orig != "Film Sack" || w.StartMs != 0 || w.EndMs != 1500 || len(us[0].Words) != 2 {
		t.Fatalf("merged word %+v", us[0].Words)
	}
}
