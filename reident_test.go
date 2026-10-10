package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStampChanged(t *testing.T) {
	const base = "t=0.55|1:600:r|2:30:-"
	cases := []struct {
		name, old, cur string
		want           bool
	}{
		{"same", base, base, false},
		{"never looked at", "", base, true},
		{"one more passage for somebody who has ten minutes", base, "t=0.55|1:640:r|2:30:-", false},
		{"a third more for somebody", base, "t=0.55|1:800:r|2:30:-", true},
		{"a quarter less (a wrong passage removed)", base, "t=0.55|1:440:r|2:30:-", true},
		{"15 s more for somebody who has 30 s: under the 20 s floor", base, "t=0.55|1:600:r|2:45:-", false},
		{"25 s more for somebody who has 30 s", base, "t=0.55|1:600:r|2:55:-", true},
		{"a new person", base, base + "|3:90:-", true},
		{"a person gone", base, "t=0.55|1:600:r", true},
		{"roster changed", base, "t=0.55|1:600:-|2:30:-", true},
		{"threshold changed", base, "t=0.60|1:600:r|2:30:-", true},
		{"unreadable old stamp", "garbage", base, true},
	}
	for _, c := range cases {
		if got := stampChanged(c.old, c.cur); got != c.want {
			t.Errorf("%s: stampChanged = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIdentStampFor(t *testing.T) {
	sec := map[int64]int{9: 120, 2: 45, 5: 700}
	roster := map[int64]string{2: "host"}
	if got, want := identStampFor(sec, roster, false, nil, 0.55), "t=0.55|2:45:r|5:700:-|9:120:-"; got != want {
		t.Errorf("stamp = %q, want %q", got, want)
	}
	// an episode limited to some people only counts those
	if got, want := identStampFor(sec, roster, true, map[int64]bool{5: true}, 0.55), "t=0.55|5:700:-"; got != want {
		t.Errorf("limited stamp = %q, want %q", got, want)
	}
	if got := identStampFor(nil, nil, false, nil, 0.6); got != "t=0.60" {
		t.Errorf("empty stamp = %q", got)
	}
}

// a finished episode with a saved audio file, in a podcast, plus a person with voice data
func reidentFixture(t *testing.T) (st *Store, ep Episode, ver, person int64, restore func()) {
	t.Helper()
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	restore = func() { P.DB = old }
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	feed, err := st.AddFeed(Feed{URL: "https://x/a.xml", Title: "A", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertEpisodes(feed, 0, []FeedItem{{GUID: "g1", Title: "Episode one", PubDate: 1000, AudioURL: "https://x/1.mp3"}}); err != nil {
		t.Fatal(err)
	}
	st.db.QueryRow(`SELECT id FROM episodes WHERE guid='g1'`).Scan(&ep.ID)
	ver, err = st.CreateVersion(Version{EpisodeID: ep.ID, WhisperModel: "m", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	st.db.Exec(`UPDATE versions SET status='done', audio_file='1.m4a' WHERE id=?`, ver)
	if err := st.FinishEpisode(ep.ID, ver); err != nil {
		t.Fatal(err)
	}
	ep, _ = st.Episode(ep.ID)
	person, err = st.AddPerson("Alice")
	if err != nil {
		t.Fatal(err)
	}
	return st, ep, ver, person, restore
}

func addVoice(t *testing.T, st *Store, person, ver int64, seconds float64) {
	t.Helper()
	if err := st.AddVoiceSample(VoiceSample{PersonID: person, VersionID: ver, StartMs: 0, EndMs: int64(seconds * 1000), Seconds: seconds, Source: "confirmed", Vec: []float32{1, 0}}); err != nil {
		t.Fatal(err)
	}
}

func TestStaleIdentSelection(t *testing.T) {
	st, ep, ver, person, restore := reidentFixture(t)
	defer restore()

	// never looked at: stale (this is the one look every old episode gets)
	addVoice(t, st, person, ver, 300)
	list, err := st.staleIdent(nil)
	if err != nil || len(list) != 1 || list[0].VersionID != ver || list[0].Title != "Episode one" {
		t.Fatalf("expected the one finished episode to be stale, got %+v %v", list, err)
	}
	// ... unless it failed lately
	if l, _ := st.staleIdent(map[int64]time.Time{ver: time.Now()}); len(l) != 0 {
		t.Fatal("an episode that just failed is tried again at once")
	}
	if l, _ := st.staleIdent(map[int64]time.Time{ver: time.Now().Add(-reidentRetryFail - time.Minute)}); len(l) != 1 {
		t.Fatal("an episode that failed long ago isn't tried again")
	}

	// looked at with today's voice data: not stale
	stamp, err := st.identStamp(ep)
	if err != nil {
		t.Fatal(err)
	}
	st.SetIdentStamp(ver, stamp)
	if l, _ := st.staleIdent(nil); len(l) != 0 {
		t.Fatalf("a freshly stamped episode is stale: %+v", l)
	}

	// a little more voice data: still fine; a lot more: stale again
	addVoice(t, st, person, ver, 30) // 300 -> 330 s = +10 %
	if l, _ := st.staleIdent(nil); len(l) != 0 {
		t.Fatal("+10 % voice data made the episode stale")
	}
	addVoice(t, st, person, ver, 100) // 330 -> 430 s = +43 % since the stamp
	if l, _ := st.staleIdent(nil); len(l) != 1 {
		t.Fatal("+43 % voice data didn't make the episode stale")
	}

	// a new person with voice data counts too
	st.SetIdentStamp(ver, func() string { s, _ := st.identStamp(ep); return s }())
	bob, _ := st.AddPerson("Bob")
	addVoice(t, st, bob, ver, 60)
	if l, _ := st.staleIdent(nil); len(l) != 1 {
		t.Fatal("a new person with voice data didn't make the episode stale")
	}

	// episodes that can't be redone are never listed: no saved audio, not finished
	st.db.Exec(`UPDATE versions SET audio_file='' WHERE id=?`, ver)
	if l, _ := st.staleIdent(nil); len(l) != 0 {
		t.Fatal("an episode without saved audio is listed")
	}
	st.db.Exec(`UPDATE versions SET audio_file='1.m4a', status='running' WHERE id=?`, ver)
	if l, _ := st.staleIdent(nil); len(l) != 0 {
		t.Fatal("an unfinished version is listed")
	}
}

func TestReidentifyWaitsForQuiet(t *testing.T) {
	st, _, ver, person, restore := reidentFixture(t)
	defer restore()
	srv := &Server{st: st, worker: newWorker(st)}
	started := time.Now()

	if !reidentReady(srv, started) { // no voice data at all: nothing recent
		t.Fatal("not ready although nobody added anything")
	}
	addVoice(t, st, person, ver, 60)
	if reidentReady(srv, started) {
		t.Fatal("ready right after somebody confirmed a voice")
	}
	// six hours later it is
	st.db.Exec(`UPDATE voice_samples SET created_at=?`, time.Now().Add(-reidentQuiet-time.Minute).Unix())
	if !reidentReady(srv, started) {
		t.Fatal("not ready after the quiet time")
	}
	// ... but a sample added while a pass runs stops that pass
	addVoice(t, st, person, ver, 10)
	if reidentReady(srv, started) {
		t.Fatal("a pass goes on although new voice data arrived")
	}
}

// a re-run that can't finish must leave the names that are there
func TestIdentifyFailureKeepsOldNames(t *testing.T) {
	st, ep, ver, person, restore := reidentFixture(t)
	defer restore()
	addVoice(t, st, person, ver, 60)
	st.db.Exec(`UPDATE versions SET audio_file='' WHERE id=?`, ver) // no saved audio: the run fails
	v, _ := st.Version(ver)

	auto, _ := st.AddCorrectionID(ver, Correction{Kind: "range", StartMs: 0, EndMs: 5000, Label: 1, Auto: true})
	hand, _ := st.AddCorrectionID(ver, Correction{Kind: "range", StartMs: 6000, EndMs: 9000, Label: 2})

	if _, err := identifyVersion(t.Context(), st, v, nil); err == nil {
		t.Fatal("expected an error without saved audio")
	}
	cs, _ := st.Corrections(ver)
	have := map[int64]bool{}
	for _, c := range cs {
		have[c.ID] = true
	}
	if !have[auto] || !have[hand] {
		t.Fatalf("a failed run lost corrections (automatic kept: %v, manual kept: %v)", have[auto], have[hand])
	}
	if got := st.IdentStamp(ver); got != "" {
		t.Fatalf("a failed run was stamped as done: %q", got)
	}
	_ = ep
}

func TestReplaceAutoCorrections(t *testing.T) {
	st, _, ver, _, restore := reidentFixture(t)
	defer restore()
	st.AddCorrectionID(ver, Correction{Kind: "range", StartMs: 0, EndMs: 1000, Label: 11, Auto: true})
	st.AddCorrectionID(ver, Correction{Kind: "range", StartMs: 2000, EndMs: 3000, Label: 12}) // by hand
	st.AddCorrectionID(ver, Correction{Kind: "merge", From: 1, Label: 13, Auto: true})        // another kind
	err := st.ReplaceAutoCorrections(ver, "range", []Correction{{Kind: "range", StartMs: 500, EndMs: 900, Label: 14, Auto: true}})
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := st.Corrections(ver)
	labels := map[string]bool{}
	for _, c := range cs {
		labels[c.Kind+":"+strconv.Itoa(c.Label)] = true
	}
	// 11 = the old automatic range (gone), 12 = by hand (kept), 13 = another kind (kept), 14 = the new one
	if labels["range:11"] || !labels["range:14"] || !labels["range:12"] || !labels["merge:13"] || len(cs) != 3 {
		t.Fatalf("after replacing: %v", labels)
	}
}

func TestReidentifySetupSection(t *testing.T) {
	st, _, ver, person, restore := reidentFixture(t)
	defer restore()
	st.AddUser("admin", "longenough1", roleAdmin)
	addVoice(t, st, person, ver, 120)

	srv := &Server{st: st, worker: newWorker(st), serverMode: true}
	if err := srv.loadTemplates(); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	c := &http.Client{Jar: mustJar(t), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.PostForm(ts.URL+"/login", url.Values{"name": {"admin"}, "password": {"longenough1"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	get := func(path string) string {
		resp, err := c.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	body := get("/setup")
	if !complete(body) || !strings.Contains(body, `id="reidentify"`) || !strings.Contains(body, "Look again at older episodes automatically") ||
		!strings.Contains(body, "1 episode waiting for a new look") || !strings.Contains(body, "Somebody confirmed a voice recently") {
		t.Fatalf("setup section: complete %v, has section %v, waiting text %v, quiet note %v", complete(body),
			strings.Contains(body, `id="reidentify"`), strings.Contains(body, "1 episode waiting"), strings.Contains(body, "Somebody confirmed"))
	}
	if !strings.Contains(body, `name="on" value="1" checked`) {
		t.Fatal("the switch is not on by default")
	}

	// switch off (an unticked box sends nothing)
	resp, err = c.PostForm(ts.URL+"/settings/reidentify", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "/setup") || st.Setting("auto_reidentify", "1") != "0" {
		t.Fatalf("switching off: status %d, location %q, setting %q", resp.StatusCode, resp.Header.Get("Location"), st.Setting("auto_reidentify", "1"))
	}
	if body = get("/setup"); !strings.Contains(body, "Switched off.") || strings.Contains(body, `name="on" value="1" checked`) {
		t.Fatal("the page doesn't show the switch as off")
	}
	// and a pass does nothing while it is off
	failed := map[int64]time.Time{}
	reidentifyPass(t.Context(), srv, failed)
	if len(failed) != 0 {
		t.Fatal("a pass ran although the switch is off")
	}
}

// the local app has no "Older episodes" section (it is for servers) and its
// Setup page must still come out whole
func TestSetupPageInLocalMode(t *testing.T) {
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	defer func() { P.DB = old }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{st: st, worker: newWorker(st)}
	if err := srv.loadTemplates(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/setup", nil)
	req.Host = "127.0.0.1:8321"
	srv.routes().ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 || !complete(body) || strings.Contains(body, `id="reidentify"`) {
		t.Fatalf("local Setup page: status %d, complete %v, has the server-only section %v", rec.Code, complete(body), strings.Contains(body, `id="reidentify"`))
	}
}
