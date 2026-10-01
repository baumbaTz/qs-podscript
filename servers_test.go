package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSavedServers(t *testing.T) {
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	defer func() { P.DB = old }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	// a connection from before 0.26.0 moves into the list
	st.SetSetting("remote_url", "https://pod.example.org")
	st.SetSetting("remote_token", "tok")
	st.SetSetting("remote_user", "batz")
	st.SetSetting("remote_work", "1")
	list := savedServers(st)
	if len(list) != 1 || list[0].ID != 1 || !list[0].Work || list[0].User != "batz" || st.Setting("remote_url", "") != "" {
		t.Fatalf("migrated: %+v", list)
	}
	if list[0].Label() != "pod.example.org" || !list[0].IsAdmin() {
		t.Fatalf("label/admin: %q %v", list[0].Label(), list[0].IsAdmin())
	}
	// a second one; the same address again only renews the key
	id2 := addServer(st, remoteConf{URL: "https://other.example.org", Token: "t2", User: "b"})
	if again := addServer(st, remoteConf{URL: "https://pod.example.org", Token: "new", User: "batz"}); again != 1 {
		t.Fatalf("same address got id %d", again)
	}
	if c, _ := serverByID(st, 1); c.Token != "new" || !c.Work {
		t.Fatalf("renewed: %+v", c)
	}
	updateServer(st, id2, func(c *remoteConf) { c.Name = "Friends" })
	if ps := placeList(st); len(ps) != 3 || !ps[0].Active || ps[2].Name != "Friends" {
		t.Fatalf("places: %+v", ps)
	}
	setActivePlace(st, id2)
	if c, ok := activeServer(st); !ok || c.ID != id2 {
		t.Fatal("active server")
	}
	// the menu travels to the server in a header
	if ps := decodePlaces(encodePlaces(placeList(st))); len(ps) != 3 || !ps[2].Active || ps[0].Active {
		t.Fatalf("header: %+v", ps)
	}
	st.addAsk(ask{Server: true, SrvID: id2, ID: 9})
	removeServer(st, id2)
	if _, ok := activeServer(st); ok || activePlace(st) != 0 {
		t.Fatal("removed active server: back to this computer")
	}
	if len(st.asks()) != 0 {
		t.Fatal("its asked-for entries stay")
	}
	for _, p := range []string{"/", "/feeds/3", "/episodes/2", "/search", "/people/4"} {
		if !contentPage(p) {
			t.Errorf("%s should lead to the server", p)
		}
	}
	for _, p := range []string{"/setup", "/queue", "/log", "/help", "/static/app.css", "/events"} {
		if contentPage(p) {
			t.Errorf("%s should stay local", p)
		}
	}
}

func TestClaimTakesTurns(t *testing.T) {
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	defer func() { P.DB = old }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	serve := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"job": Job{Lease: name}})
		}))
	}
	a, b := serve("A"), serve("B")
	defer a.Close()
	defer b.Close()
	addServer(st, remoteConf{URL: a.URL, Token: "t", Work: true})
	idB := addServer(st, remoteConf{URL: b.URL, Token: "t", Work: true})
	addServer(st, remoteConf{URL: "http://127.0.0.1:1", Token: "t"}) // not ticked: never asked
	var got string
	for i := 0; i < 4; i++ {
		_, job, err := claimRemoteJob(context.Background(), st)
		if err != nil || job == nil {
			t.Fatalf("claim %d: %v %v", i, job, err)
		}
		got += job.Lease
	}
	if got != "ABAB" && got != "BABA" {
		t.Errorf("servers should take turns, got %s", got)
	}
	// B down: A still gets asked
	b.Close()
	for i := 0; i < 2; i++ {
		if _, job, _ := claimRemoteJob(context.Background(), st); job == nil || job.Lease != "A" {
			t.Fatalf("with B down: %+v", job)
		}
	}
	updateServer(st, idB, func(c *remoteConf) { c.Work = false })
	a.Close()
	if _, job, err := claimRemoteJob(context.Background(), st); job != nil || err == nil {
		t.Fatalf("all ticked servers down: %v %v", job, err)
	}
}
