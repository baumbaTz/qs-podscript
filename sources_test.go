package main

import "testing"

func TestMultiFeedDedup(t *testing.T) {
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	defer func() { P.DB = old }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.AddFeed(Feed{URL: "https://x/main.xml", Title: "Show", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFeed(Feed{URL: "https://x/main.xml"}); err == nil {
		t.Fatal("duplicate feed accepted")
	}
	srcs, _ := st.Sources(id)
	if len(srcs) != 1 || !srcs[0].Main {
		t.Fatalf("sources %+v", srcs)
	}
	day := int64(86400)
	n, _ := st.UpsertEpisodes(id, srcs[0].ID, []FeedItem{
		{GUID: "a", Title: "Ep 10", PubDate: 100 * day, AudioURL: "https://cdn/x/ep10-abcdef.mp3?t=1"},
		{GUID: "b", Title: "Ep 11", PubDate: 107 * day, AudioURL: "https://cdn/x/ep11-abcdef.mp3"},
	})
	if n != 2 {
		t.Fatalf("main added %d", n)
	}
	arch, err := st.AddSource(id, "https://x/archive.xml", "Archive")
	if err != nil {
		t.Fatal(err)
	}
	n, _ = st.UpsertEpisodes(id, arch, []FeedItem{
		{GUID: "old-a", Title: "Episode 10 (old url)", PubDate: 100 * day, AudioURL: "https://old/ep10-abcdef.mp3"}, // same file
		{GUID: "old-b", Title: "Ep 11", PubDate: 107*day + 3600, AudioURL: "https://old/other.mp3"},                 // same title+day
		{GUID: "a", Title: "Ep 10 changed", PubDate: 100 * day, AudioURL: "https://old/zzz.mp3"},                    // same guid, other feed
		{GUID: "c", Title: "Ep 1", PubDate: 10 * day, AudioURL: "https://old/ep1-abcdef.mp3"},
	})
	if n != 1 {
		t.Fatalf("archive added %d, want 1", n)
	}
	eps, _ := st.Episodes(id, "", 0)
	if len(eps) != 3 || eps[0].Title != "Ep 1" || eps[0].SourceID != arch || eps[1].Title != "Ep 10" {
		t.Fatalf("episodes %+v", eps)
	}
	// removing the archive removes its untranscribed episode, keeps the others
	if _, err := st.RemoveSource(id, srcs[0].ID+999); err == nil {
		t.Fatal("removed unknown source")
	}
	removed, err := st.RemoveSource(id, arch)
	if err != nil || removed != 1 {
		t.Fatalf("removed %d, %v", removed, err)
	}
	if _, err := st.RemoveSource(id, srcs[0].ID); err == nil {
		t.Fatal("removed the last feed")
	}
}
