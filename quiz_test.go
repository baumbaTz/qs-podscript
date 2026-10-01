package main

import (
	"fmt"
	"testing"
)

func quizUtt(label int, startMs int64, n int, stepMs int64, sentenceEvery int) Utterance {
	u := Utterance{Label: label, StartMs: startMs}
	t := startMs
	for i := 0; i < n; i++ {
		w := fmt.Sprintf("w%d", i)
		if sentenceEvery > 0 && (i+1)%sentenceEvery == 0 {
			w += "."
		}
		u.Words = append(u.Words, Word{Text: w, StartMs: t, EndMs: t + stepMs - 50, P: 1})
		t += stepMs
	}
	u.EndMs = t
	return u
}

func TestQuizRowsAndWindows(t *testing.T) {
	us := []Utterance{
		quizUtt(0, 0, 40, 400, 8),                  // 16 s, sentences every 3.2 s
		quizUtt(personLabel(3), 16000, 60, 500, 0), // 30 s monologue without punctuation
		{Label: 1, StartMs: 46000, EndMs: 60000, Mark: markAd, Words: []Word{{Text: "ad", StartMs: 46000, EndMs: 60000}}},
		quizUtt(1, 60000, 20, 400, 5),
	}
	rows := quizRows(us)
	for _, r := range rows {
		if r.EndMs-r.StartMs > quizRowMaxMs {
			t.Fatalf("row too long: %+v", r.EndMs-r.StartMs)
		}
		if r.StartMs >= 46000 && r.StartMs < 60000 {
			t.Fatal("ad passage must be left out")
		}
	}
	// sentence ends split the first utterance into rows of >= 2.5 s
	if rows[0].EndMs-rows[0].StartMs < quizRowMinMs || rows[0].Label != 0 {
		t.Fatalf("first row: %+v", rows[0])
	}
	wins := quizWindows(rows)
	var covered int
	for i, w := range wins {
		if w.EndMs-w.StartMs > quizWinMaxMs+8000 {
			t.Fatalf("window %d too long: %d", i, w.EndMs-w.StartMs)
		}
		covered += len(w.Rows)
	}
	if covered != len(rows) {
		t.Fatalf("rows lost: %d of %d", covered, len(rows))
	}
	if wins[0].Score < 3 {
		t.Fatalf("unnamed voice should score high: %d", wins[0].Score)
	}
	// the 14 s ad gap ends a passage
	for _, w := range wins {
		if w.StartMs < 46000 && w.EndMs > 60000 {
			t.Fatal("passage spans the ad gap")
		}
	}
}

func TestQuizCoverage(t *testing.T) {
	ivs := mergeIntervals([]interval{{10, 20}, {15, 30}, {40, 50}})
	if len(ivs) != 2 || ivs[0] != (interval{10, 30}) {
		t.Fatalf("merge: %v", ivs)
	}
	if got := covered(ivs, 0, 45); got != 25 {
		t.Fatalf("covered: %d", got)
	}
	w := quizWindow{StartMs: 10, EndMs: 35}
	if !w.checked(ivs) { // 20 of 25 = 80 %
		t.Fatal("80% should count as checked")
	}
	if (quizWindow{StartMs: 0, EndMs: 40}).checked(ivs) {
		t.Fatal("50% is not checked")
	}
	if percent(999, 1000) != 99 || percent(0, 0) != 0 || percent(5, 10) != 50 {
		t.Fatal("percent")
	}
	if labelValue(personLabel(7)) != "p7" || labelValue(labelCrosstalk) != "x" || labelValue(2) != "2" {
		t.Fatal("labelValue")
	}
}

func TestCarryChecksSkipsUnnamedVoices(t *testing.T) {
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	defer func() { P.DB = old }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.db.Exec(`INSERT INTO feeds(id,url,title,created_at) VALUES(1,'u','F',0)`)
	st.db.Exec(`INSERT INTO episodes(id,feed_id,guid,title,audio_url,status,pub_date) VALUES(1,1,'g','E','a','done',0)`)
	for _, id := range []int{1, 2} {
		if _, err := st.db.Exec(`INSERT INTO versions(id,episode_id,created_at,status,whisper_model,whisper_backend,language) VALUES(?,1,0,'done','m','b','en')`, id); err != nil {
			t.Fatal(err)
		}
	}
	st.db.Exec(`INSERT INTO checks(version_id,start_ms,end_ms,user_id,changed,created_at) VALUES(1,0,30000,7,1,5)`)
	shown := []Utterance{
		{StartMs: 0, EndMs: 10000, Label: personLabel(3)},
		{StartMs: 10000, EndMs: 18000, Label: 2}, // unnamed voice: not carried
		{StartMs: 18000, EndMs: 30000, Label: labelCrosstalk},
	}
	n, err := carryChecks(st, Version{ID: 1}, Version{ID: 2}, shown)
	if err != nil || n != 2 {
		t.Fatalf("carried %d, %v", n, err)
	}
	ivs := st.checkedIntervals(2, nil)
	if len(ivs) != 2 || ivs[0] != (interval{0, 10000}) || ivs[1] != (interval{18000, 30000}) {
		t.Fatalf("carried parts: %v", ivs)
	}
	if st.checksBy(7) != 1 {
		t.Fatal("carried checks must not count twice for the helper")
	}
	if !hasManualCorrections(st, 1) {
		t.Fatal("checks alone must trigger carrying over")
	}
}
