package main

import "testing"

func markTestData() ([]Segment, []Token, []Turn) {
	segs := []Segment{{Idx: 0, StartMs: 0, EndMs: 6000, Text: " a b c d e f"}}
	var toks []Token
	for i, w := range []string{" a", " b", " c", " d", " e", " f"} {
		toks = append(toks, Token{SegIdx: 0, Text: w, StartMs: int64(i) * 1000, EndMs: int64(i)*1000 + 800, P: 0.9})
	}
	turns := []Turn{{StartMs: 0, EndMs: 6000, Cluster: 0}}
	return segs, toks, turns
}

func TestMarksSplitParagraphKeepSpeaker(t *testing.T) {
	segs, toks, turns := markTestData()
	corr := []Correction{{Kind: "mark", StartMs: 1900, EndMs: 4000, Text: markAd}}
	us := buildUtterances(segs, toks, turns, corr)
	if len(us) != 3 {
		t.Fatalf("want 3 paragraphs, got %d: %+v", len(us), us)
	}
	want := []struct {
		text, mark string
	}{{"a b", ""}, {"c d", markAd}, {"e f", ""}}
	for i, w := range want {
		if us[i].Text != w.text || us[i].Mark != w.mark || us[i].Label != 0 {
			t.Errorf("paragraph %d = %q mark %q label %d, want %q %q 0", i, us[i].Text, us[i].Mark, us[i].Label, w.text, w.mark)
		}
	}
}

func TestMarkClearedByLaterNormal(t *testing.T) {
	segs, toks, turns := markTestData()
	corr := []Correction{
		{Kind: "mark", StartMs: 0, EndMs: 6000, Text: markClip},
		{Kind: "mark", StartMs: 2000, EndMs: 6000, Text: ""},
	}
	us := buildUtterances(segs, toks, turns, corr)
	if len(us) != 2 || us[0].Mark != markClip || us[0].Text != "a b" || us[1].Mark != "" {
		t.Fatalf("got %+v", us)
	}
	iv := markedIntervals(corr, markClip)
	if len(iv) != 1 || iv[0].a != 0 || iv[0].b != 2000 {
		t.Fatalf("clip intervals %+v", iv)
	}
	if s := markedShare(iv, 1000, 3000); s != 0.5 {
		t.Fatalf("share %v", s)
	}
}

func TestMarksAreCarried(t *testing.T) {
	segs, toks, turns := markTestData()
	corr := []Correction{{Kind: "mark", StartMs: 1000, EndMs: 3000, Text: markAd}}
	plan := planCarry(segs, toks, turns, corr)
	found := false
	for _, c := range plan {
		if c.Kind == "mark" && c.Text == markAd && c.StartMs == 1000 && c.EndMs == 3000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("mark not carried: %+v", plan)
	}
}

func TestClusterClipShare(t *testing.T) {
	turns := []Turn{{StartMs: 0, EndMs: 1000, Cluster: 1}, {StartMs: 5000, EndMs: 8000, Cluster: 1}, {StartMs: 1000, EndMs: 5000, Cluster: 0}}
	clips := []interval{{5000, 9000}}
	if s := clusterClipShare(turns, clips, 1); s != 0.75 {
		t.Fatalf("share %v", s)
	}
	if s := clusterClipShare(turns, clips, 0); s != 0 {
		t.Fatalf("share %v", s)
	}
}

func TestDeleteVoiceSamplesIn(t *testing.T) {
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	defer func() { P.DB = old }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := st.AddPerson("Test")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range [][2]int64{{0, 4000}, {9000, 13000}, {11000, 20000}} {
		if err := st.AddVoiceSample(VoiceSample{PersonID: pid, VersionID: 7, StartMs: r[0], EndMs: r[1], Seconds: 4, Source: "confirmed", Vec: []float32{1, 0}}); err != nil {
			t.Fatal(err)
		}
	}
	// clip 10-16 s: covers 3/4 of the second sample, 5/9 of the third, none of the first
	n, err := st.DeleteVoiceSamplesIn(7, 10000, 16000)
	if err != nil || n != 2 {
		t.Fatalf("deleted %d, err %v", n, err)
	}
	left, _ := st.VoiceSamples(pid)
	if len(left) != 1 || left[0].StartMs != 0 {
		t.Fatalf("left %+v", left)
	}
}
