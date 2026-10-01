package main

import (
	"context"
	"strings"
	"testing"
)

// one episode, two whisper segments; the second one is marked as an ad
func searchTestStore(t *testing.T) (*Store, int64, int64, int64) {
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	t.Cleanup(func() { P.DB = old })
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	f, _ := st.AddFeed(Feed{URL: "https://x/f.xml", Title: "Show", Language: "en"})
	srcs, _ := st.Sources(f)
	st.UpsertEpisodes(f, srcs[0].ID, []FeedItem{{GUID: "a", Title: "Pilot", PubDate: 1000, AudioURL: "https://x/a.mp3"}})
	eps, _ := st.Episodes(f, "", 10)
	ep := eps[0]
	vid, _ := st.CreateVersion(Version{EpisodeID: ep.ID, WhisperModel: "m"})
	texts := []string{"Welcome to film sack, the movie café podcast. Don't panic.", "This episode is brought to you by Squarespace."}
	var segs []Segment
	var toks []Token
	ms := int64(0)
	for i, tx := range texts {
		start := ms
		for j, w := range strings.Fields(tx) {
			toks = append(toks, Token{SegIdx: i, Idx: j, StartMs: ms, EndMs: ms + 400, Text: " " + w, P: 0.9})
			ms += 500
		}
		segs = append(segs, Segment{Idx: i, StartMs: start, EndMs: ms, Text: tx})
	}
	if err := st.SaveTranscription(vid, segs, toks); err != nil {
		t.Fatal(err)
	}
	st.SaveDiarization(vid, []Turn{{0, ms, 0}}, nil)
	st.FinishVersion(Version{ID: vid, EpisodeID: ep.ID, Status: "done", AudioSeconds: float64(ms) / 1000})
	st.FinishEpisode(ep.ID, vid)
	st.AddCorrection(vid, Correction{Kind: "mark", StartMs: segs[1].StartMs, EndMs: segs[1].EndMs, Text: markAd})
	return st, f, ep.ID, vid
}

func TestSearch(t *testing.T) {
	st, f, epID, _ := searchTestStore(t)
	if err := initSearch(st); err != nil {
		t.Fatal(err)
	}
	t.Logf("FTS5: %v", search.fts)
	if n, err := updateSearchIndex(context.Background(), st); err != nil || n != 1 {
		t.Fatalf("index: %d %v", n, err)
	}
	if n, _ := updateSearchIndex(context.Background(), st); n != 0 {
		t.Fatalf("nothing changed, but %d episodes indexed again", n)
	}
	find := func(q string) int {
		hs, total, err := runSearch(st, searchQuery{Q: q}, 30, 0)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if total > 0 && (hs[0].EpisodeID != epID || hs[0].FeedID != f) {
			t.Fatalf("%q: wrong hit %+v", q, hs[0])
		}
		return total
	}
	for q, want := range map[string]int{
		"film sack":      1,
		`"movie cafe"`:   1, // accents don't matter
		"MOVIE":          1,
		"mov*":           1,
		"mov":            0, // whole words only
		"don't":          1,
		"squarespace":    0, // ads are left out
		`"sack film"`:    0,
		`") OR (`:        0, // no query syntax gets through
		"panic welcome":  1,
		"panic nonsense": 0,
	} {
		if got := find(q); got != want {
			t.Errorf("%q: %d hits, want %d", q, got, want)
		}
	}
	// a spelling fix changes what is found - after the index noticed it
	st.AddSpellingRule(SpellingRule{Correct: "FilmSack", Variants: []string{"film sack"}})
	if n, _ := updateSearchIndex(context.Background(), st); n != 1 {
		t.Fatalf("spelling fix: %d episodes indexed again", n)
	}
	if find("filmsack") != 1 || find(`"film sack"`) != 0 {
		t.Error("spelling fix not in the index")
	}
	hs, _, _ := runSearch(st, searchQuery{Q: "filmsack"}, 30, 0)
	if !strings.Contains(string(hs[0].Snippet()), "<mark>FilmSack") {
		t.Errorf("snippet %q", hs[0].Snippet())
	}
	// highlighting on the episode page
	us := []Utterance{{Words: []Word{{Text: "Welcome"}, {Text: "FilmSack,"}, {Text: "movies"}}}}
	markSearchHits(us, "filmsack mov*")
	if us[0].Words[0].Hit || !us[0].Words[1].Hit || !us[0].Words[2].Hit {
		t.Errorf("hits: %+v", us[0].Words)
	}
	// episode gone -> rows gone
	st.db.Exec(`DELETE FROM episodes WHERE id=?`, epID)
	updateSearchIndex(context.Background(), st)
	if find("filmsack") != 0 {
		t.Error("deleted episode still found")
	}
}

func TestSearchSel(t *testing.T) {
	for v, want := range map[string][2]int64{"s5": {1, 5}, "l7": {0, 7}, "9": {0, 9}, "": {0, 0}, "sx": {0, 0}} {
		srv, id := searchSel(v)
		if (srv && want[0] != 1) || (!srv && want[0] == 1) || id != want[1] {
			t.Errorf("%q: %v %d", v, srv, id)
		}
	}
}
