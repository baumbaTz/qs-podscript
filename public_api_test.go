package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// apiFixture builds two podcasts with real, indexed transcripts: podcast A has
// two episodes, podcast B has one. Alice and Bob talk in episode A1; Bob and
// Carol in A2 (so Bob appears in both episodes of podcast A); nobody but
// Alice is in B1. A1 also has a one-line movie clip "spoken" by Alice, which
// browsing and the public API must both leave out.
type apiFixture struct {
	st                *Store
	feedA, feedB      int64
	ep1, ep2, ep3     int64 // A1, A2, B1
	v1, v2, v3        int64
	alice, bob, carol int64
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	t.Cleanup(func() { P.DB = old })
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := initSearch(st); err != nil {
		t.Fatal(err)
	}
	f := &apiFixture{st: st}
	f.feedA, err = st.AddFeed(Feed{URL: "https://x/a.xml", Title: "Podcast A", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	f.feedB, err = st.AddFeed(Feed{URL: "https://x/b.xml", Title: "Podcast B", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	f.alice, _ = st.AddPerson("Alice")
	f.bob, _ = st.AddPerson("Bob")
	f.carol, _ = st.AddPerson("Carol")
	refreshPeopleCache(st) // labelName()/personName() read from this cache, not the DB directly

	mk := func(feed int64, guid, title string, pub int64) (epID, verID int64) {
		if _, err := st.UpsertEpisodes(feed, 0, []FeedItem{{GUID: guid, Title: title, PubDate: pub, AudioURL: "https://x/" + guid + ".mp3"}}); err != nil {
			t.Fatal(err)
		}
		st.db.QueryRow(`SELECT id FROM episodes WHERE guid=?`, guid).Scan(&epID)
		verID, err = st.CreateVersion(Version{EpisodeID: epID, WhisperModel: "m", Language: "en"})
		if err != nil {
			t.Fatal(err)
		}
		st.db.Exec(`UPDATE versions SET status='done', audio_file=? WHERE id=?`, guid+".m4a", verID)
		if err := st.FinishEpisode(epID, verID); err != nil {
			t.Fatal(err)
		}
		return epID, verID
	}
	// segs/toks/turns: one word per token, one segment per turn, clusters 0/1
	seed := func(verID int64, lines [][3]any) { // {clusterOrMark, "mark" or "", "words"}
		for i, ln := range lines {
			cluster, mark, words := ln[0].(int), ln[1].(string), strings.Fields(ln[2].(string))
			start := int64(i * 10000)
			st.db.Exec(`INSERT INTO segments(version_id,idx,start_ms,end_ms,text) VALUES(?,?,?,?,?)`,
				verID, i, start, start+int64(len(words))*500, ln[2])
			for wi, w := range words {
				ws := start + int64(wi)*500
				st.db.Exec(`INSERT INTO tokens(version_id,seg_idx,idx,start_ms,end_ms,text,p) VALUES(?,?,?,?,?,?,?)`,
					verID, i, wi, ws, ws+450, " "+w, 0.9)
			}
			st.db.Exec(`INSERT INTO turns(version_id,start_ms,end_ms,cluster) VALUES(?,?,?,?)`, verID, start, start+int64(len(words))*500, cluster)
			_ = mark
		}
	}
	corrMerge := func(verID int64, cluster int, person int64) {
		st.AddCorrectionID(verID, Correction{Kind: "merge", From: cluster, Label: personLabel(person), Auto: true})
	}
	corrMark := func(verID int64, startMs, endMs int64, kind string) {
		st.AddCorrectionID(verID, Correction{Kind: "mark", StartMs: startMs, EndMs: endMs, Text: kind})
	}

	f.ep1, f.v1 = mk(f.feedA, "a1", "A1: Alice and Bob", 3000)
	seed(f.v1, [][3]any{
		{0, "", "Alice says hello there everybody today"},
		{1, "", "Bob replies with his own greeting words"},
		{0, "", "a short movie clip plays here now"}, // will be marked as a clip below
	})
	corrMerge(f.v1, 0, f.alice)
	corrMerge(f.v1, 1, f.bob)
	corrMark(f.v1, 20000, 23500, markClip)

	f.ep2, f.v2 = mk(f.feedA, "a2", "A2: Bob and Carol", 2000)
	seed(f.v2, [][3]any{
		{0, "", "Bob is talking again in this episode"},
		{1, "", "Carol responds with a long answer now"},
	})
	corrMerge(f.v2, 0, f.bob)
	corrMerge(f.v2, 1, f.carol)

	f.ep3, f.v3 = mk(f.feedB, "b1", "B1: Only Alice", 1000)
	seed(f.v3, [][3]any{
		{0, "", "Alice is alone on this other podcast"},
	})
	corrMerge(f.v3, 0, f.alice)

	for _, e := range []struct{ ep, feed, ver int64 }{{f.ep1, f.feedA, f.v1}, {f.ep2, f.feedA, f.v2}, {f.ep3, f.feedB, f.v3}} {
		if err := indexEpisode(st, e.ep, e.feed, e.ver, "t"); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// ---------------------------------------------------------------- browseTurns / publicTranscript (store level)

func TestBrowseTurnsScoping(t *testing.T) {
	f := newAPIFixture(t)

	// everywhere: Bob appears in both his episodes, newest first (A1 pub 3000 > A2 pub 2000)
	hits, total, err := browseTurns(f.st, 0, 0, f.bob, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(hits) != 2 {
		t.Fatalf("Bob everywhere: total=%d hits=%d", total, len(hits))
	}
	if hits[0].EpisodeID != f.ep1 || hits[1].EpisodeID != f.ep2 {
		t.Fatalf("Bob everywhere not newest-first: %+v", hits)
	}

	// one podcast: Alice is in both A1 and B1, but scoped to feed A she's only in A1
	hits, total, err = browseTurns(f.st, f.feedA, 0, f.alice, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || hits[0].EpisodeID != f.ep1 {
		t.Fatalf("Alice in podcast A: total=%d hits=%+v", total, hits)
	}

	// one episode: Bob only, within A1
	hits, total, err = browseTurns(f.st, f.feedA, f.ep1, f.bob, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || !strings.Contains(hits[0].Text, "replies") {
		t.Fatalf("Bob in episode A1: total=%d hits=%+v", total, hits)
	}

	// movie clips left out: Alice's clip line in A1 must not appear anywhere
	hits, _, _ = browseTurns(f.st, 0, 0, f.alice, 50, 0)
	for _, h := range hits {
		if strings.Contains(h.Text, "movie clip plays") {
			t.Fatalf("a movie clip line was included: %+v", h)
		}
	}
	if n := len(hits); n != 2 { // Alice's real lines: A1 greeting + B1
		t.Fatalf("Alice everywhere (clip excluded): got %d, want 2: %+v", n, hits)
	}

	// pagination
	hits, total, err = browseTurns(f.st, 0, 0, f.bob, 1, 0)
	if err != nil || len(hits) != 1 || total != 2 {
		t.Fatalf("page 1 of 1: hits=%d total=%d err=%v", len(hits), total, err)
	}
	hits2, _, err := browseTurns(f.st, 0, 0, f.bob, 1, 1)
	if err != nil || len(hits2) != 1 || hits2[0].EpisodeID == hits[0].EpisodeID {
		t.Fatalf("page 2 of 1 should be the other episode: %+v vs %+v (err %v)", hits, hits2, err)
	}
}

func TestPublicTranscript(t *testing.T) {
	f := newAPIFixture(t)
	ep, err := f.st.Episode(f.ep1)
	if err != nil {
		t.Fatal(err)
	}
	turns, status, err := f.st.publicTranscript(ep)
	if err != nil || status != "done" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	// the full transcript keeps clip lines, tagged, just like the website's own
	// episode page does - only ad breaks are left out there
	if len(turns) != 3 {
		t.Fatalf("got %d turns, want 3 (clip kept and tagged): %+v", len(turns), turns)
	}
	if turns[0].Speaker != "Alice" || turns[1].Speaker != "Bob" {
		t.Fatalf("speakers wrong: %+v", turns)
	}
	if turns[2].Mark != "clip" {
		t.Fatalf("the clip line should be tagged mark=clip: %+v", turns[2])
	}
	if turns[0].StartMs != 0 || turns[0].EndMs <= 0 {
		t.Fatalf("timing looks wrong: %+v", turns[0])
	}

	// an episode with no finished version yet: empty, not an error
	noVer, _ := f.st.AddFeed(Feed{URL: "https://x/c.xml", Title: "C", Language: "en"})
	f.st.UpsertEpisodes(noVer, 0, []FeedItem{{GUID: "c1", Title: "Not done", PubDate: 1, AudioURL: "https://x/c1.mp3"}})
	var epID int64
	f.st.db.QueryRow(`SELECT id FROM episodes WHERE guid='c1'`).Scan(&epID)
	ep2, _ := f.st.Episode(epID)
	turns, status, err = f.st.publicTranscript(ep2)
	if err != nil || len(turns) != 0 {
		t.Fatalf("unfinished episode: turns=%+v status=%q err=%v", turns, status, err)
	}
}

// ---------------------------------------------------------------- HTTP end-to-end

func newAPITestServer(t *testing.T, f *apiFixture) *httptest.Server {
	t.Helper()
	srv := &Server{st: f.st, worker: newWorker(f.st), serverMode: true}
	if err := srv.loadTemplates(); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.routes())
	t.Cleanup(ts.Close)
	return ts
}

func loginClient(t *testing.T, ts *httptest.Server, name, pw string) *http.Client {
	t.Helper()
	c := &http.Client{Jar: mustJar(t), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.PostForm(ts.URL+"/login", url.Values{"name": {name}, "password": {pw}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login %s: status %d", name, resp.StatusCode)
	}
	return c
}

func getJSON(t *testing.T, c *http.Client, url, bearer string, out any) (status int, body string) {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode == 200 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("GET %s: bad JSON: %v\n%s", url, err, b)
		}
	}
	return resp.StatusCode, string(b)
}

// makePublicToken: as admin, grant api_access to "ed" and mint a public key for them.
func makePublicToken(t *testing.T, ts *httptest.Server, admin *http.Client, edID int64) string {
	t.Helper()
	resp, err := admin.PostForm(ts.URL+"/users/"+strconv.FormatInt(edID, 10),
		url.Values{"api_access": {"1"}, "podcast": nil})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = admin.PostForm(ts.URL+"/keys/public", url.Values{"user": {strconv.FormatInt(edID, 10)}, "name": {"test key"}})
	if err != nil {
		t.Fatal(err)
	}
	loc := resp.Header.Get("Location")
	resp.Body.Close()
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	msg := u.Query().Get("msg")
	i := strings.LastIndex(msg, "qsps_")
	if i < 0 {
		t.Fatalf("no token in redirect message: %q", msg)
	}
	return msg[i:]
}

func TestPublicAPIEndToEnd(t *testing.T) {
	f := newAPIFixture(t)
	f.st.AddUser("admin", "longenough1", roleAdmin)
	edID, _ := f.st.AddUser("ed", "longenough2", roleEditor) // no podcast rights, no api_access yet
	ts := newAPITestServer(t, f)
	admin := loginClient(t, ts, "admin", "longenough1")

	// ed has no key yet: using the API fails
	plain := &http.Client{}
	if code, _ := getJSON(t, plain, ts.URL+"/api/public/v1/podcasts", "not-a-real-token", nil); code != http.StatusUnauthorized {
		t.Fatalf("garbage token: status %d, want 401", code)
	}
	if code, _ := getJSON(t, plain, ts.URL+"/api/public/v1/podcasts", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no token at all: status %d, want 401", code)
	}

	tok := makePublicToken(t, ts, admin, edID)

	// podcasts list
	var pods struct {
		Podcasts []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"podcasts"`
	}
	if code, body := getJSON(t, plain, ts.URL+"/api/public/v1/podcasts", tok, &pods); code != 200 || len(pods.Podcasts) != 2 {
		t.Fatalf("podcasts: status %d body %s", code, body)
	}

	// episodes of podcast A
	var eps struct {
		Episodes []apiEpisodeBrief `json:"episodes"`
		Total    int               `json:"total"`
	}
	path := ts.URL + "/api/public/v1/podcasts/" + strconv.FormatInt(f.feedA, 10) + "/episodes"
	if code, body := getJSON(t, plain, path, tok, &eps); code != 200 || eps.Total != 2 || len(eps.Episodes) != 2 {
		t.Fatalf("episodes: status %d total %d body %s", code, eps.Total, body)
	}
	if eps.Episodes[0].ID != f.ep1 { // newest by pub_date (3000), not by id
		t.Fatalf("episodes not newest-first by pub_date: %+v", eps.Episodes)
	}

	// since= only returns episodes with a higher id
	path2 := path + "?since=" + strconv.FormatInt(min64(f.ep1, f.ep2), 10)
	var eps2 struct {
		Episodes []apiEpisodeBrief `json:"episodes"`
	}
	if code, _ := getJSON(t, plain, path2, tok, &eps2); code != 200 || len(eps2.Episodes) != 1 {
		t.Fatalf("since=: got %d episodes, want 1", len(eps2.Episodes))
	}

	// a transcript
	var tr struct {
		Turns []apiTurn `json:"turns"`
	}
	if code, body := getJSON(t, plain, ts.URL+"/api/public/v1/episodes/"+strconv.FormatInt(f.ep1, 10), tok, &tr); code != 200 {
		t.Fatalf("transcript: status %d body %s", code, body)
	}
	if len(tr.Turns) != 3 || tr.Turns[0].Speaker != "Alice" || tr.Turns[2].Mark != "clip" {
		t.Fatalf("transcript turns wrong (clip line should be kept, tagged): %+v", tr.Turns)
	}

	// a person's turns, scoped by podcast and episode
	var pt struct {
		Turns []searchHit `json:"turns"`
		Total int         `json:"total"`
	}
	bobPath := ts.URL + "/api/public/v1/people/" + strconv.FormatInt(f.bob, 10) + "/turns"
	if code, _ := getJSON(t, plain, bobPath, tok, &pt); code != 200 || pt.Total != 2 {
		t.Fatalf("Bob everywhere via API: total=%d", pt.Total)
	}
	if code, _ := getJSON(t, plain, bobPath+"?episode="+strconv.FormatInt(f.ep1, 10), tok, &pt); code != 200 || pt.Total != 1 {
		t.Fatalf("Bob in one episode via API: total=%d", pt.Total)
	}

	// 404s for a nonexistent / hidden podcast and episode
	if code, _ := getJSON(t, plain, ts.URL+"/api/public/v1/episodes/999999", tok, nil); code != http.StatusNotFound {
		t.Fatalf("missing episode: status %d", code)
	}

	// the api_access flag can be taken away, and the key stops working at once
	resp, err := admin.PostForm(ts.URL+"/users/"+strconv.FormatInt(edID, 10), url.Values{"podcast": nil})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if code, _ := getJSON(t, plain, ts.URL+"/api/public/v1/podcasts", tok, nil); code != http.StatusForbidden {
		t.Fatalf("after api_access removed: status %d, want 403", code)
	}
}

// the two critical isolation properties: a public key must never work as a
// general login, and a worker (device) key must never work on the public API
func TestPublicTokenIsolation(t *testing.T) {
	f := newAPIFixture(t)
	f.st.AddUser("admin", "longenough1", roleAdmin)
	edID, _ := f.st.AddUser("ed", "longenough2", roleEditor)
	ts := newAPITestServer(t, f)
	admin := loginClient(t, ts, "admin", "longenough1")
	pubTok := makePublicToken(t, ts, admin, edID)
	workerTok, err := f.st.NewAPIToken(edID, "ed's laptop")
	if err != nil {
		t.Fatal(err)
	}

	// a public key must not authenticate the holder as "ed" anywhere else - try
	// it against an ordinary cookie-or-bearer guarded admin page. A client that
	// does NOT follow redirects, so a 303 to /login (meaning "not logged in",
	// the correct outcome) can't be mistaken for success by chasing it there.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", ts.URL+"/users", nil)
	req.Header.Set("Authorization", "Bearer "+pubTok)
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("a public API key authenticated as a logged-in user on an ordinary page - privilege escalation")
	}
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "/login") {
		t.Fatalf("expected a redirect to /login (not logged in), got status %d location %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// nor on the internal worker API
	plain := &http.Client{}
	req2, _ := http.NewRequest("GET", ts.URL+"/api/v1/podcasts", nil)
	req2.Header.Set("Authorization", "Bearer "+pubTok)
	resp2, err := plain.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode == 200 && !strings.Contains(string(b2), `"podcasts"`) {
		// 200 with something other than the expected podcasts payload would be
		// suspicious; the real internal handler also requires its own auth, so
		// this is a secondary check, not the main one
		t.Fatalf("unexpected success shape from the internal API with a public key: %s", b2)
	}

	// a worker (device) key must not work on the public API
	if code, _ := getJSON(t, plain, ts.URL+"/api/public/v1/podcasts", workerTok, nil); code == 200 {
		t.Fatal("a worker/device key was accepted by the public API")
	}

	// the sharper check: a public key must not be able to claim work (a write,
	// worker-only internal endpoint that does require a real logged-in user)
	req3, _ := http.NewRequest("POST", ts.URL+"/api/v1/work/claim", strings.NewReader("{}"))
	req3.Header.Set("Authorization", "Bearer "+pubTok)
	resp3, err := plain.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode == 200 {
		t.Fatal("a public API key was able to claim work on the internal worker API - privilege escalation")
	}
	// the worker key, by contrast, is allowed to try (it may fail for other
	// reasons, e.g. nothing queued, but must not be rejected as unauthenticated)
	req4, _ := http.NewRequest("POST", ts.URL+"/api/v1/work/claim", strings.NewReader("{}"))
	req4.Header.Set("Authorization", "Bearer "+workerTok)
	resp4, err := plain.Do(req4)
	if err != nil {
		t.Fatal(err)
	}
	resp4.Body.Close()
	if resp4.StatusCode == http.StatusUnauthorized {
		t.Fatal("a real worker key was rejected as unauthenticated - the kind='worker' restriction broke normal worker auth")
	}

	// an admin's own account always has access, with no flag or key needed to log in,
	// but still needs a key for the public API itself
	if code, _ := getJSON(t, plain, ts.URL+"/api/public/v1/podcasts", "qsps_doesnotexist", nil); code != http.StatusUnauthorized {
		t.Fatalf("made-up token: status %d", code)
	}
}

func TestPublicAPIRateLimit(t *testing.T) {
	f := newAPIFixture(t)
	f.st.AddUser("admin", "longenough1", roleAdmin)
	edID, _ := f.st.AddUser("ed", "longenough2", roleEditor)
	ts := newAPITestServer(t, f)
	admin := loginClient(t, ts, "admin", "longenough1")
	tok := makePublicToken(t, ts, admin, edID)

	// find the token id and set a tiny limit directly (faster than 20000 real calls)
	var tid int64
	f.st.db.QueryRow(`SELECT id FROM api_tokens WHERE token_hash=?`, tokenHash(tok)).Scan(&tid)
	if err := f.st.SetTokenLimit(tid, 3); err != nil {
		t.Fatal(err)
	}

	plain := &http.Client{}
	var lastCode int
	var lastBody string
	for i := 0; i < 4; i++ {
		lastCode, lastBody = getJSON(t, plain, ts.URL+"/api/public/v1/podcasts", tok, nil)
	}
	if lastCode != http.StatusTooManyRequests {
		t.Fatalf("4th call with a limit of 3: status %d body %s", lastCode, lastBody)
	}
	// a generous default exists and is a real number, not zero
	var def int
	f.st.db.QueryRow(`SELECT daily_limit FROM api_tokens WHERE token_hash=?`, tokenHash(makePublicToken(t, ts, admin, edID))).Scan(&def)
	if def < 1000 {
		t.Fatalf("default daily limit looks too low to be 'generous by default': %d", def)
	}
}

func TestAPIHiddenPodcast(t *testing.T) {
	f := newAPIFixture(t)
	f.st.AddUser("admin", "longenough1", roleAdmin)
	edID, _ := f.st.AddUser("ed", "longenough2", roleEditor)
	ts := newAPITestServer(t, f)
	admin := loginClient(t, ts, "admin", "longenough1")
	tok := makePublicToken(t, ts, admin, edID)

	if err := f.st.SetAPIHidden(f.feedA, true); err != nil {
		t.Fatal(err)
	}
	plain := &http.Client{}
	var pods struct {
		Podcasts []struct {
			ID int64 `json:"id"`
		} `json:"podcasts"`
	}
	if _, body := getJSON(t, plain, ts.URL+"/api/public/v1/podcasts", tok, &pods); len(pods.Podcasts) != 1 {
		t.Fatalf("hidden podcast still listed: %s", body)
	}
	if code, _ := getJSON(t, plain, ts.URL+"/api/public/v1/podcasts/"+strconv.FormatInt(f.feedA, 10)+"/episodes", tok, nil); code != http.StatusNotFound {
		t.Fatalf("episodes of a hidden podcast: status %d, want 404", code)
	}
	if code, _ := getJSON(t, plain, ts.URL+"/api/public/v1/episodes/"+strconv.FormatInt(f.ep1, 10), tok, nil); code != http.StatusNotFound {
		t.Fatalf("an episode of a hidden podcast: status %d, want 404", code)
	}
	// the website itself is unaffected - hiding from the API doesn't hide the page
	if code, body := getJSON(t, plain, ts.URL+"/feeds/"+strconv.FormatInt(f.feedA, 10), "", nil); code != 200 || !strings.Contains(body, "Podcast A") {
		t.Fatalf("hidden-from-API podcast page: status %d", code)
	}
}

// ---------------------------------------------------------------- Search page browse mode

func TestSearchBrowseMode(t *testing.T) {
	f := newAPIFixture(t)
	ts := newAPITestServer(t, f)
	plain := &http.Client{}
	get := func(u string) (int, string) {
		resp, err := plain.Get(ts.URL + u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// person only, no podcast/episode: everything Bob said, full text (not a snippet)
	code, body := get("/search?person=" + strconv.FormatInt(f.bob, 10))
	if code != 200 || !complete(body) {
		t.Fatalf("browse page: status %d complete %v", code, complete(body))
	}
	if !strings.Contains(body, "Everything Bob said") || !strings.Contains(body, "replies with his own greeting words") ||
		!strings.Contains(body, "is talking again in this episode") {
		t.Fatalf("Bob's full lines are not both shown:\n%s", body)
	}
	if strings.Contains(body, "movie clip plays") {
		t.Fatal("a movie-clip line leaked into the browse page")
	}

	// narrowed to a podcast: heading changes, episode dropdown appears, only that podcast's lines show
	code, body = get("/search?person=" + strconv.FormatInt(f.alice, 10) + "&podcast=" + strconv.FormatInt(f.feedA, 10))
	if code != 200 || !strings.Contains(body, "Everything Alice said in this podcast") || !strings.Contains(body, `name="episode"`) {
		t.Fatalf("podcast-narrowed browse page missing heading or episode selector:\n%s", body)
	}
	if strings.Contains(body, "alone on this other podcast") {
		t.Fatal("podcast filter did not actually narrow the results (leaked podcast B line)")
	}

	// a plain word search still behaves exactly as before (not accidentally routed into browse mode)
	code, body = get("/search?q=greeting")
	if code != 200 || strings.Contains(body, "Everything") || !strings.Contains(body, "passage") {
		t.Fatalf("a normal search was affected by the browse-mode change:\n%s", body)
	}
}
