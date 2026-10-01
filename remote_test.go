package main

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLeases(t *testing.T) {
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	defer func() { P.DB = old }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	f, _ := st.AddFeed(Feed{URL: "https://x/f.xml", Title: "Show", Language: "en"})
	srcs, _ := st.Sources(f)
	st.UpsertEpisodes(f, srcs[0].ID, []FeedItem{{GUID: "a", Title: "A", PubDate: 1000, AudioURL: "https://x/a-long-name.mp3"}})
	u1, _ := st.AddUser("one", "longenough1", roleEditor)
	u2, _ := st.AddUser("two", "longenough2", roleEditor)

	job, err := st.ClaimLease(u1, "pc", version, claimFilter{})
	if err != nil || job == nil || job.Title != "A" || job.Language != "en" {
		t.Fatalf("claim: %+v %v", job, err)
	}
	if j2, _ := st.ClaimLease(u2, "pc2", version, claimFilter{}); j2 != nil {
		t.Fatal("same episode handed out twice")
	}
	if err := st.ExtendLease(job.Lease, u2); err == nil {
		t.Fatal("someone else extended the lease")
	}
	if err := st.ExtendLease(job.Lease, u1); err != nil {
		t.Fatal(err)
	}
	// expired lease -> back in the queue for the next worker
	st.db.Exec(`UPDATE episodes SET lease_expires=? WHERE id=?`, time.Now().Add(-time.Minute).Unix(), job.EpisodeID)
	j2, _ := st.ClaimLease(u2, "pc2", version, claimFilter{})
	if j2 == nil || j2.EpisodeID != job.EpisodeID {
		t.Fatal("expired lease not handed out again")
	}
	if _, err := st.leaseEpisode(job.Lease, u1); err == nil {
		t.Fatal("old lease still valid")
	}
	// failures (the expiry above counted as one): after leaseMaxFails attempts
	// the episode is marked failed
	if err := st.FailLease(j2.Lease, u2, "boom"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < leaseMaxFails; i++ {
		j, _ := st.ClaimLease(u1, "pc", version, claimFilter{})
		if j == nil {
			break
		}
		st.FailLease(j.Lease, u1, "boom")
	}
	ep, _ := st.Episode(job.EpisodeID)
	if ep.Status != "error" {
		t.Fatalf("status after repeated failures: %s", ep.Status)
	}
	tok, _ := st.NewAPIToken(u1, "pc")
	if u := st.TokenUser(tok); u == nil || u.Name != "one" {
		t.Fatal("token user")
	}
	if !versionAtLeast("0.13.0", "0.13.0") || versionAtLeast("0.12.9", "0.13.0") || !versionAtLeast("1.0.0", "0.13.0") {
		t.Fatal("versionAtLeast")
	}
}

func TestDiarizeJobs(t *testing.T) {
	old, oldAudio := P.DB, P.Audio
	dir := t.TempDir()
	P.DB, P.Audio = dir+"/t.db", dir
	defer func() { P.DB, P.Audio = old, oldAudio }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	f, _ := st.AddFeed(Feed{URL: "https://x/f.xml", Title: "Show", Language: "en"})
	srcs, _ := st.Sources(f)
	st.UpsertEpisodes(f, srcs[0].ID, []FeedItem{{GUID: "a", Title: "A", PubDate: 1000, AudioURL: "https://x/a-long-name.mp3"}})
	eps, _ := st.Episodes(f, "", 10)
	ep := eps[0]
	vid, _ := st.CreateVersion(Version{EpisodeID: ep.ID, WhisperModel: "m", AudioFile: "ep1_v1.ogg"})
	st.FinishVersion(Version{ID: vid, EpisodeID: ep.ID, Status: "done", AudioFile: "ep1_v1.ogg", AudioSeconds: 60})
	st.FinishEpisode(ep.ID, vid)
	os.WriteFile(dir+"/ep1_v1.ogg", []byte("x"), 0o644)
	u, _ := st.AddUser("one", "longenough1", roleEditor)

	if err := st.QueueRediarize(ep.ID, vid); err != nil {
		t.Fatal(err)
	}
	// an own queue may take it too (processQueued only redoes the speaker detection)
	if next, ok, _ := st.NextEpisode(0, false); !ok || next.ID != ep.ID {
		t.Fatalf("speaker detection job not in the queue: %v %+v", ok, next)
	}
	var kind string
	st.db.QueryRow(`SELECT job_kind FROM episodes WHERE id=?`, ep.ID).Scan(&kind)
	if kind != "diarize" {
		t.Fatalf("job kind %q", kind)
	}
	if j, _ := st.ClaimLease(u, "old", "0.18.1", claimFilter{}); j != nil {
		t.Fatal("an old worker got a speaker detection job")
	}
	j, err := st.ClaimLease(u, "new", "0.19.0", claimFilter{})
	if err != nil || j == nil || j.Kind != jobDiarize || j.SourceVersion != vid || j.AudioURL != "/audio/ep1_v1.ogg" {
		t.Fatalf("claim: %+v %v", j, err)
	}
	// a failure puts it back for another try - it doesn't just drop the request
	if err := st.FailLease(j.Lease, u, "boom"); err != nil {
		t.Fatal(err)
	}
	e, _ := st.Episode(ep.ID)
	if kind, _ := st.episodeJob(ep.ID); e.Status != "queued" || kind != jobDiarize {
		t.Fatalf("after one failure: %s %q", e.Status, kind)
	}
	j, _ = st.ClaimLease(u, "new", "0.19.0", claimFilter{})
	if j == nil {
		t.Fatal("not handed out again")
	}
	res := &JobResult{Kind: jobDiarize, AudioSeconds: 60, NumClusters: 1,
		Turns: []Turn{{StartMs: 0, EndMs: 5000, Cluster: 0}}}
	v, err := storeDiarizeResult(context.Background(), st, e, vid, res, u, "one", "new")
	if err != nil {
		t.Fatal(err)
	}
	e, _ = st.Episode(ep.ID)
	if kind, _ := st.episodeJob(ep.ID); e.Status != "done" || kind != "" || e.ActiveVersionID.Int64 != v.ID || v.AudioFile != "ep1_v1.ogg" {
		t.Fatalf("after result: %+v kind=%q v=%+v", e, kind, v)
	}
}

func TestClaimOnePodcast(t *testing.T) {
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	defer func() { P.DB = old }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	f1, _ := st.AddFeed(Feed{URL: "https://x/1.xml", Title: "One", Language: "en"})
	f2, _ := st.AddFeed(Feed{URL: "https://x/2.xml", Title: "Two", Language: "en"})
	s1, _ := st.Sources(f1)
	s2, _ := st.Sources(f2)
	st.UpsertEpisodes(f1, s1[0].ID, []FeedItem{{GUID: "a", Title: "Old one", PubDate: 1000, AudioURL: "https://x/a.mp3"}})
	st.UpsertEpisodes(f2, s2[0].ID, []FeedItem{{GUID: "b", Title: "B1", PubDate: 2000, AudioURL: "https://x/b1.mp3"},
		{GUID: "c", Title: "B2", PubDate: 3000, AudioURL: "https://x/b2.mp3"}})
	u, _ := st.AddUser("one", "longenough1", roleEditor)
	var got []string
	for {
		j, err := st.ClaimLease(u, "pc", version, claimFilter{FeedID: f2})
		if err != nil {
			t.Fatal(err)
		}
		if j == nil {
			break
		}
		got = append(got, j.Title)
	}
	if len(got) != 2 || got[0] != "B1" || got[1] != "B2" {
		t.Fatalf("podcast Two only, oldest first: %v", got)
	}
	// skipping: B1 failed on this computer -> B2 next, then nothing
	st.db.Exec(`UPDATE episodes SET status='new', lease_hash='' WHERE feed_id=?`, f2)
	eps, _ := st.Episodes(f2, "", 10)
	var b1 int64
	for _, e := range eps {
		if e.Title == "B1" {
			b1 = e.ID
		}
	}
	got = nil
	for {
		j, _ := st.ClaimLease(u, "pc", version, claimFilter{FeedID: f2, Skip: []int64{b1}})
		if j == nil {
			break
		}
		got = append(got, j.Title)
	}
	if len(got) != 1 || got[0] != "B2" {
		t.Fatalf("podcast Two only, oldest first: %v", got)
	}
	if j, _ := st.ClaimLease(u, "pc", version, claimFilter{}); j == nil || j.Title != "Old one" {
		t.Fatalf("the rest stays for everybody: %+v", j)
	}
}
