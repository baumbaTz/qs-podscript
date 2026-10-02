package main

import "testing"

func TestTimingSteps(t *testing.T) {
	tot, wh, di := timingSteps("download 4s, convert 2s, diarize 49m3s, whisper 6m12s, identify 1s, audio 3s, worker turbo cuda")
	if tot != 4+2+2943+372+1+3 || wh != 372 || di != 2943 {
		t.Fatalf("got %d %d %d", tot, wh, di)
	}
	if tot, _, _ := timingSteps("transcript from v3, download 1s, diarize 1m0s, speakers by x"); tot != 61 {
		t.Fatalf("redo: %d", tot)
	}
}

func TestComputerWorkCredit(t *testing.T) {
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	defer func() { P.DB = old }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, _ := st.AddUser("anna", "password123", "editor")
	b, _ := st.AddUser("ben", "password123", "editor")
	f, _ := st.AddFeed(Feed{URL: "https://example.org/f.xml", Title: "F"})
	srcs, _ := st.Sources(f)
	st.UpsertEpisodes(f, srcs[0].ID, []FeedItem{{GUID: "1", Title: "E1", PubDate: 1, AudioURL: "https://example.org/1.mp3"}})
	eps, _ := st.Episodes(f, "", 5)
	add := func(v Version, timing string) {
		v.EpisodeID = eps[0].ID
		v.ID, _ = st.CreateVersion(v)
		st.FinishVersion(Version{ID: v.ID, Status: "done", AudioSeconds: 3600, Timing: timing})
	}
	add(Version{TranscribedBy: a, TranscribedOn: "pc-a"}, "download 10s, diarize 20m0s, whisper 10m0s")
	// ben redoes the speakers (since 0.31.0: credited to ben)
	add(Version{TranscribedBy: a, TranscribedOn: "pc-a", SpeakersBy: b, SpeakersOn: "pc-b"}, "transcript from v1, download 5s, diarize 15m0s, speakers by pc-b")
	// an old redo by a helper: unknown whose, left out
	add(Version{TranscribedBy: a, TranscribedOn: "pc-a"}, "transcript from v1, download 5s, diarize 15m0s, speakers by pc-x")
	sa, sb := st.UserStats(a), st.UserStats(b)
	if sa.Transcribed != 1 || sa.Speakers != 0 || sa.BusySec != 1810 || sa.WhisperSec != 600 || len(sa.Computers) != 1 {
		t.Fatalf("anna: %+v", sa)
	}
	if sb.Transcribed != 0 || sb.Speakers != 1 || sb.BusySec != 905 || sb.Computers[0].Name != "pc-b" || sb.Computers[0].Speed() != "4.0×" {
		t.Fatalf("ben: %+v", sb)
	}
}
