package main

import (
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Spelling fixes: fixed replacement rules applied when a transcript is shown
// or downloaded ("film sack", "Filmsack" -> "FilmSack"). The raw transcript
// is never changed, so a new rule fixes every episode at once and deleting a
// rule brings the original back.
//
// Matching is deliberately strict, so only safe replacements happen:
//   - whole words only, case doesn't matter, punctuation around the words stays
//   - a variant of several words must appear as exactly those words in a row
//   - a trailing possessive 's is kept ("film sack's" -> "FilmSack's")
//
// Rules belong to one podcast or to all podcasts. Full names (2+ words) of all
// known people are automatic rules: "brian dunaway" -> "Brian Dunaway".

type SpellingRule struct {
	ID       int64
	FeedID   int64 // 0 = all podcasts
	Correct  string
	Variants []string
	Auto     bool // from a person's name, not stored
}

func (s *Store) SpellingRules(feedID int64) ([]SpellingRule, error) {
	rows, err := s.db.Query(`SELECT id, feed_id, correct, variants FROM spelling
		WHERE feed_id=0 OR feed_id=? ORDER BY correct COLLATE NOCASE`, feedID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpellingRule
	for rows.Next() {
		var r SpellingRule
		var vs string
		if err := rows.Scan(&r.ID, &r.FeedID, &r.Correct, &vs); err != nil {
			return nil, err
		}
		r.Variants = splitVariants(vs)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) AddSpellingRule(r SpellingRule) error {
	_, err := s.db.Exec(`INSERT INTO spelling(feed_id, correct, variants, created_at) VALUES(?,?,?,?)`,
		r.FeedID, r.Correct, strings.Join(r.Variants, "\n"), time.Now().Unix())
	return err
}

// SetSpellingScope moves a rule to one podcast (feedID) or to all (0).
func (s *Store) SetSpellingScope(id, feedID int64) error {
	_, err := s.db.Exec(`UPDATE spelling SET feed_id=? WHERE id=?`, feedID, id)
	return err
}

func (s *Store) DeleteSpellingRule(id int64) error {
	_, err := s.db.Exec(`DELETE FROM spelling WHERE id=?`, id)
	return err
}

// splitVariants accepts variants separated by commas, semicolons or lines.
func splitVariants(s string) []string {
	f := func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' }
	var out []string
	seen := map[string]bool{}
	for _, v := range strings.FieldsFunc(s, f) {
		v = strings.Join(strings.Fields(v), " ")
		k := strings.ToLower(v)
		if v != "" && !seen[k] {
			seen[k] = true
			out = append(out, v)
		}
	}
	return out
}

// nameRules: automatic rules for known people with a full name.
func nameRules(people []Person) []SpellingRule {
	var out []SpellingRule
	for _, p := range people {
		name := strings.Join(strings.Fields(p.Name), " ")
		if len(strings.Fields(name)) >= 2 {
			out = append(out, SpellingRule{Correct: name, Variants: []string{name}, Auto: true})
		}
	}
	return out
}

// spellingFor returns all rules that apply to a podcast (its own, the global
// ones and the people's names).
func spellingFor(st *Store, feedID int64) []SpellingRule {
	rules, _ := st.SpellingRules(feedID)
	people, _ := st.People()
	return append(rules, nameRules(people)...)
}

// spellNorm: lower case, letters/digits/apostrophes only ("Sack," -> "sack").
func spellNorm(w string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(w) {
		switch {
		case r == '’' || r == '\'':
			b.WriteRune('\'')
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "'")
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// edges splits a word into leading punctuation, core and trailing punctuation.
func edges(w string) (lead, core, trail string) {
	a := strings.IndexFunc(w, isWordRune)
	if a < 0 {
		return w, "", ""
	}
	b := strings.LastIndexFunc(w, isWordRune)
	_, size := utf8.DecodeRuneInString(w[b:])
	return w[:a], w[a : b+size], w[b+size:]
}

type spellPattern struct {
	tokens  []string
	correct string
}

func compileSpelling(rules []SpellingRule) []spellPattern {
	var ps []spellPattern
	seen := map[string]bool{}
	for _, r := range rules {
		for _, v := range r.Variants {
			var toks []string
			for _, w := range strings.Fields(v) {
				if n := spellNorm(w); n != "" {
					toks = append(toks, n)
				}
			}
			key := strings.Join(toks, " ")
			if len(toks) == 0 || seen[key] {
				continue // first rule wins for the same variant
			}
			seen[key] = true
			ps = append(ps, spellPattern{toks, r.Correct})
		}
	}
	// longest variants first: "film sack podcast" before "film sack"
	sort.SliceStable(ps, func(i, j int) bool { return len(ps[i].tokens) > len(ps[j].tokens) })
	return ps
}

// applySpelling replaces matching words in the shown transcript.
//
// A text correction is one Word holding several words ("Welcome to Film
// Sack"); its words are matched too, but a match never reaches across its
// edge, and it stays one Word afterwards.
func applySpelling(us []Utterance, rules []SpellingRule) []Utterance {
	ps := compileSpelling(rules)
	if len(ps) == 0 {
		return us
	}
	type unit struct {
		text  string
		norm  string
		word  int // index of the Word it comes from
		multi bool
	}
	for ui := range us {
		words := us[ui].Words
		var units []unit
		for wi, w := range words {
			parts := strings.Fields(w.Text)
			if len(parts) <= 1 {
				units = append(units, unit{w.Text, spellNorm(w.Text), wi, false})
				continue
			}
			for _, p := range parts {
				units = append(units, unit{p, spellNorm(p), wi, true})
			}
		}
		// out: replaced units; each keeps the range of words it came from
		type piece struct {
			text     string
			from, to int // word indexes
			orig     string
			multi    bool
		}
		var out []piece
		changed := false
		for i := 0; i < len(units); {
			matched := false
			for _, p := range ps {
				n := len(p.tokens)
				if i+n > len(units) {
					continue
				}
				ok, possessive := true, false
				for k, t := range p.tokens {
					u := units[i+k]
					if u.multi != units[i].multi || (u.multi && u.word != units[i].word) {
						ok = false // never across the edge of a text correction
						break
					}
					if u.norm == t {
						continue
					}
					if k == n-1 && u.norm == t+"'s" {
						possessive = true
						continue
					}
					ok = false
					break
				}
				if !ok {
					continue
				}
				lead, _, _ := edges(units[i].text)
				_, _, trail := edges(units[i+n-1].text)
				text := lead + p.correct
				if possessive {
					text += "'s"
				}
				text += trail
				orig := make([]string, n)
				for k := 0; k < n; k++ {
					orig[k] = units[i+k].text
				}
				pc := piece{text: text, from: units[i].word, to: units[i+n-1].word, multi: units[i].multi}
				if o := strings.Join(orig, " "); o != text {
					pc.orig = o
					changed = true
				}
				out = append(out, pc)
				i += n
				matched = true
				break
			}
			if !matched {
				u := units[i]
				out = append(out, piece{text: u.text, from: u.word, to: u.word, multi: u.multi})
				i++
			}
		}
		if !changed {
			continue
		}
		var nw []Word
		for k := 0; k < len(out); k++ {
			pc := out[k]
			first, last := words[pc.from], words[pc.to]
			w := Word{Text: pc.text, StartMs: first.StartMs, EndMs: last.EndMs, P: first.P}
			for j := pc.from; j <= pc.to; j++ {
				w.P = min(w.P, words[j].P)
			}
			if pc.multi { // glue the text correction back together
				texts := []string{pc.text}
				anyChange := pc.orig != ""
				for k+1 < len(out) && out[k+1].multi && out[k+1].from == pc.from {
					k++
					texts = append(texts, out[k].text)
					anyChange = anyChange || out[k].orig != ""
				}
				w.Text = strings.Join(texts, " ")
				if anyChange {
					w.Orig = first.Text
				}
			} else {
				w.Orig = pc.orig
			}
			nw = append(nw, w)
		}
		us[ui].Words = nw
		parts := make([]string, len(nw))
		for j, w := range nw {
			parts[j] = w.Text
		}
		us[ui].Text = strings.Join(parts, " ")
	}
	return us
}
