package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// TestInviteEndToEndHTTP drives the invite feature through real HTTP
// handlers and real templates (not just ParseFiles – ExecuteTemplate, with
// the exact data the handlers build), the way a browser would: an admin
// logs in, creates an invite restricted to one podcast, the users page
// shows the link and the QR, and a second, cookie-less client opens the
// join link and creates their account. This is the layer where a map
// passed to a template that then calls a non-existent field or method
// would actually fail – ParseFiles alone can't catch that.
func TestInviteEndToEndHTTP(t *testing.T) {
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	defer func() { P.DB = old }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser("admin", "longenough1", roleAdmin); err != nil {
		t.Fatal(err)
	}
	fID, err := st.AddFeed(Feed{URL: "https://x/a.xml", Title: "A Show", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}

	srv := &Server{st: st, serverMode: true}
	if err := srv.loadTemplates(); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	jar, _ := cookiejar.New(nil)
	admin := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// log in as admin
	resp, err := admin.PostForm(ts.URL+"/login", url.Values{"name": {"admin"}, "password": {"longenough1"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status %d", resp.StatusCode)
	}

	// GET the users page once up front – the "Invite a helper" form, open
	// invites list and tiles must all render with no invite yet
	mustGet := func(c *http.Client, path string) (int, string) {
		t.Helper()
		resp, err := c.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := mustGet(admin, "/users"); code != 200 || !strings.Contains(body, "Invite a helper") || !complete(body) {
		t.Fatalf("users page (no invites yet): status %d, body has form? %v, complete? %v", code, strings.Contains(body, "Invite a helper"), complete(body))
	}

	// create an invite restricted to the one podcast
	resp, err = admin.PostForm(ts.URL+"/invites", url.Values{"podcast": {strconv.FormatInt(fID, 10)}})
	if err != nil {
		t.Fatal(err)
	}
	loc := resp.Header.Get("Location")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "/users?invited=") {
		t.Fatalf("invite creation: status %d location %q", resp.StatusCode, loc)
	}
	tok := strings.TrimPrefix(loc, "/users?invited=")

	// the users page now renders the link, the QR svg and the podcast name
	code, body := mustGet(admin, loc)
	if code != 200 {
		t.Fatalf("users page after invite: status %d", code)
	}
	if !strings.Contains(body, "<svg ") {
		t.Fatal("no QR svg on the users page")
	}
	if !strings.Contains(body, "A Show") {
		t.Fatal("invited podcast name missing from the users page")
	}
	if !strings.Contains(body, "/join/"+tok) {
		t.Fatal("invite link missing from the users page")
	}
	if !strings.Contains(body, "Open invites") || !strings.Contains(body, "Revoke") || !strings.Contains(body, "Add a user directly") || !complete(body) {
		t.Fatalf("the rest of the users page is missing (open invites %v, revoke %v, add directly %v, complete %v)",
			strings.Contains(body, "Open invites"), strings.Contains(body, "Revoke"), strings.Contains(body, "Add a user directly"), complete(body))
	}

	// a second, unauthenticated client follows the link
	guest := &http.Client{Jar: mustJar(t), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	code, body = mustGet(guest, "/join/"+tok)
	if code != 200 || !strings.Contains(body, "A Show") || !strings.Contains(body, "Create my account") || !complete(body) {
		t.Fatalf("join page: status %d, has podcast+form? %v %v", code, strings.Contains(body, "A Show"), strings.Contains(body, "Create my account"))
	}

	// submitting with too short a password re-renders the page with an
	// error, rather than silently creating a weak account
	resp, err = guest.PostForm(ts.URL+"/join/"+tok, url.Values{"name": {"volunteer"}, "password": {"short"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "err=") {
		t.Fatalf("short-password submit: status %d location %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// a real submission creates the account and logs the guest in
	resp, err = guest.PostForm(ts.URL+"/join/"+tok, url.Values{"name": {"volunteer"}, "password": {"longenoughpw"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/start" {
		t.Fatalf("join submit: status %d location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	code, body = mustGet(guest, "/account")
	if code != 200 || !strings.Contains(body, "volunteer") {
		t.Fatalf("not logged in as the new user after joining: status %d", code)
	}
	// ... and the page they are sent to greets them and tells them how to get going
	githubAPI, relCache.at, relCache.rel = "http://127.0.0.1:1", time.Time{}, nil // no real network in tests
	code, body = mustGet(guest, "/start")
	if code != 200 || !complete(body) || !strings.Contains(body, "Welcome, volunteer") || !strings.Contains(body, "<code>"+ts.URL+"</code>") ||
		!strings.Contains(body, "/releases/download/v"+version+"/qs-podscript-"+version+"-windows-x64.zip") {
		t.Fatalf("start page after joining: status %d, welcome %v, address %v, windows link %v", code,
			strings.Contains(body, "Welcome, volunteer"), strings.Contains(body, ts.URL),
			strings.Contains(body, "/releases/download/v"+version+"/qs-podscript-"+version+"-windows-x64.zip"))
	}

	// the new user really is an editor restricted to that one podcast, not
	// an admin and not able to touch other podcasts
	u, _, err := st.userByQuery(`name=?`, "volunteer")
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != roleEditor || !u.Podcasts[fID] || len(u.Podcasts) != 1 {
		t.Fatalf("new user wrong: role=%q podcasts=%+v", u.Role, u.Podcasts)
	}

	// the page is also readable without logging in
	if code, body := mustGet(mustClient(t), "/start"); code != 200 || !complete(body) || strings.Contains(body, "Welcome,") || !strings.Contains(body, "Ask the people running this site") {
		t.Fatalf("start page without login: status %d, complete %v", code, complete(body))
	}

	// the link is now used up: a third visitor sees "not open", not the form
	code, body = mustGet(mustClient(t), "/join/"+tok)
	if code != 200 || strings.Contains(body, "Create my account") || !strings.Contains(body, "not open") {
		t.Fatalf("used invite should show the gone message: status %d, still has form? %v", code, strings.Contains(body, "Create my account"))
	}
}

// a template that fails halfway still answers 200 with the part it got to –
// so also check that a whole page came out
func complete(body string) bool { return strings.HasSuffix(strings.TrimSpace(body), "</html>") }

func mustJar(t *testing.T) *cookiejar.Jar {
	t.Helper()
	j, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func mustClient(t *testing.T) *http.Client {
	return &http.Client{Jar: mustJar(t)}
}

func TestInviteFlow(t *testing.T) {
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	defer func() { P.DB = old }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	admin, err := st.AddUser("admin", "longenough1", roleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	f1, _ := st.AddFeed(Feed{URL: "https://x/a.xml", Title: "A", Language: "en"})
	f2, _ := st.AddFeed(Feed{URL: "https://x/b.xml", Title: "B", Language: "en"})

	tok, err := st.NewInvite(admin, []int64{f1})
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}

	// the invite shows up as open, with exactly the podcasts it was made for
	open, err := st.Invites()
	if err != nil || len(open) != 1 {
		t.Fatalf("Invites(): %+v %v", open, err)
	}
	if !open[0].Podcasts[f1] || open[0].Podcasts[f2] {
		t.Fatalf("wrong podcasts on invite: %+v", open[0].Podcasts)
	}

	// a wrong token is rejected, same as a non-existent one
	if _, err := st.inviteByToken("not-the-token"); err == nil {
		t.Fatal("wrong token accepted")
	}

	// accepting creates an editor with exactly the invite's podcasts, as an
	// editor – never an admin, no matter what the join form could claim
	uid, err := st.AcceptInvite(tok, "newbie", "longenoughpw")
	if err != nil {
		t.Fatal(err)
	}
	u, _, err := st.userByQuery(`id=?`, uid)
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != roleEditor {
		t.Fatalf("role = %q, want editor", u.Role)
	}
	if !u.Podcasts[f1] || u.Podcasts[f2] {
		t.Fatalf("wrong podcasts on new user: %+v", u.Podcasts)
	}

	// the token is single-use: a second accept, and a lookup, both fail
	if _, err := st.AcceptInvite(tok, "second", "longenoughpw"); err == nil {
		t.Fatal("invite accepted twice")
	}
	if _, err := st.inviteByToken(tok); err == nil {
		t.Fatal("used invite still looks open")
	}
	if open, _ := st.Invites(); len(open) != 0 {
		t.Fatalf("used invite still listed as open: %+v", open)
	}
}

func TestInviteExpiryAndRevoke(t *testing.T) {
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	defer func() { P.DB = old }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := st.AddUser("admin", "longenough1", roleAdmin)

	tok, err := st.NewInvite(admin, nil)
	if err != nil {
		t.Fatal(err)
	}
	// force it into the past, as if the TTL had elapsed
	st.db.Exec(`UPDATE invites SET expires=? WHERE token_hash=?`, time.Now().Add(-time.Minute).Unix(), tokenHash(tok))
	if _, err := st.inviteByToken(tok); err == nil {
		t.Fatal("expired invite still looks open")
	}
	if _, err := st.AcceptInvite(tok, "late", "longenoughpw"); err == nil {
		t.Fatal("expired invite accepted")
	}

	tok2, _ := st.NewInvite(admin, nil)
	open, _ := st.Invites()
	if len(open) != 1 {
		t.Fatalf("expected 1 open invite (the expired one shouldn't count), got %d", len(open))
	}
	if err := st.RevokeInvite(open[0].ID, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := st.inviteByToken(tok2); err == nil {
		t.Fatal("revoked invite still looks open")
	}
}

// TestInviteQRMatchesEncoder doesn't attempt to decode the QR symbol itself
// (that would mean re-implementing Reed-Solomon error correction in the
// test, just to maybe get it wrong there too). Instead it checks that
// inviteQR's SVG is a faithful, pixel-for-pixel rendering of the module
// matrix the trusted encoder produced for the exact same link: same dark
// squares, same quiet (white) border, nothing extra and nothing missing.
func TestInviteQRMatchesEncoder(t *testing.T) {
	link := "https://transcribe.quicksack.li/join/abcDEF123_-xyz"
	svg := string(inviteQR(link))
	if !strings.HasPrefix(svg, "<svg ") || !strings.Contains(svg, "</svg>") {
		t.Fatalf("doesn't look like an svg: %.60q", svg)
	}

	want, err := qrcode.New(link, qrcode.Medium)
	if err != nil {
		t.Fatal(err)
	}
	want.DisableBorder = true // same as inviteQR: Bitmap() returns the bare
	// matrix with no border baked in, since inviteQR adds its own 4-module
	// quiet zone by hand. Leaving this unset here would bake the library's
	// own border into "bits" and then add the test's border on top of that
	// – double-counting it, not a check of inviteQR at all.
	bits := want.Bitmap()
	n := len(bits)
	const quiet = 4
	dim := n + quiet*2

	if got := viewBoxDim(t, svg); got != dim {
		t.Fatalf("viewBox is %dx%d, want %dx%d (n=%d, quiet=%d)", got, got, dim, dim, n, quiet)
	}
	got := parseDarkSquares(t, svg)

	wantSet := map[[2]int]bool{}
	for y, row := range bits {
		for x, dark := range row {
			if dark {
				wantSet[[2]int{x + quiet, y + quiet}] = true
			}
		}
	}
	if len(got) != len(wantSet) {
		t.Fatalf("got %d dark squares, want %d", len(got), len(wantSet))
	}
	for k := range wantSet {
		if !got[k] {
			t.Fatalf("missing dark square at %v", k)
		}
	}
	for k := range got {
		if !wantSet[k] {
			t.Fatalf("unexpected dark square at %v (outside the encoder's bitmap, or inside the quiet zone)", k)
		}
		if k[0] < quiet || k[1] < quiet || k[0] >= dim-quiet || k[1] >= dim-quiet {
			t.Fatalf("dark square at %v falls in the quiet zone", k)
		}
	}
}

func viewBoxDim(t *testing.T, svg string) int {
	t.Helper()
	i := strings.Index(svg, `viewBox="0 0 `)
	if i < 0 {
		t.Fatal("no viewBox attribute")
	}
	rest := svg[i+len(`viewBox="0 0 `):]
	fields := strings.Fields(rest[:strings.Index(rest, `"`)])
	if len(fields) != 2 {
		t.Fatalf("unexpected viewBox contents: %q", rest[:20])
	}
	w, err1 := strconv.Atoi(fields[0])
	h, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil || w != h {
		t.Fatalf("bad or non-square viewBox: %q x %q", fields[0], fields[1])
	}
	return w
}

// parseDarkSquares reads the "M<x> <y>h1v1h-1z" pieces of the <path> back
// into a set of module coordinates, independently of how inviteQR built it.
func parseDarkSquares(t *testing.T, svg string) map[[2]int]bool {
	t.Helper()
	i := strings.Index(svg, ` d="`)
	if i < 0 {
		t.Fatal("no path data attribute")
	}
	data := svg[i+4:]
	data = data[:strings.Index(data, `"`)]
	out := map[[2]int]bool{}
	for _, piece := range strings.Split(data, "M") {
		if piece == "" {
			continue
		}
		coords := strings.Fields(strings.SplitN(piece, "h", 2)[0])
		if len(coords) != 2 {
			t.Fatalf("malformed path piece %q", piece)
		}
		x, err1 := strconv.Atoi(coords[0])
		y, err2 := strconv.Atoi(coords[1])
		if err1 != nil || err2 != nil {
			t.Fatalf("non-numeric coords in %q", piece)
		}
		k := [2]int{x, y}
		if out[k] {
			t.Fatalf("square %v emitted twice", k)
		}
		out[k] = true
	}
	return out
}

// TestChangedPagesRenderCompletely opens, as the real handlers serve them, the
// pages that were reworked in the last versions, for an admin and for an
// editor – each must come out whole (not just start and then stop at a
// template error) and show the parts that belong to that role.
func TestChangedPagesRenderCompletely(t *testing.T) {
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	defer func() { P.DB = old }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	st.AddUser("admin", "longenough1", roleAdmin)
	edID, _ := st.AddUser("ed", "longenough2", roleEditor)
	f1, _ := st.AddFeed(Feed{URL: "https://x/a.xml", Title: "Zed Show", Language: "en"})
	f2, _ := st.AddFeed(Feed{URL: "https://x/b.xml", Title: "Alpha Show", Language: "en"})
	st.SetUserPodcasts(edID, []int64{f1})

	srv := &Server{st: st, serverMode: true}
	if err := srv.loadTemplates(); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	login := func(name, pw string) *http.Client {
		c := &http.Client{Jar: mustJar(t), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := c.PostForm(ts.URL+"/login", url.Values{"name": {name}, "password": {pw}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return c
	}
	get := func(c *http.Client, path string) (int, string) {
		resp, err := c.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	has := func(who, path, body string, want ...string) {
		t.Helper()
		if !complete(body) {
			t.Errorf("%s %s: page is cut off", who, path)
		}
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s %s: missing %q", who, path, w)
			}
		}
	}
	admin, editor := login("admin", "longenough1"), login("ed", "longenough2")
	fid := func(id int64) string { return strconv.FormatInt(id, 10) }

	// users: tiles in alphabetical order, each leading to the user's page
	_, body := get(admin, "/users")
	has("admin", "/users", body, `class="panel user-tile"`, "Invite a helper", "Add a user directly")
	if strings.Index(body, ">admin<") > strings.Index(body, ">ed<") {
		t.Error("users are not sorted by name")
	}

	// the user's page: rights (podcasts alphabetical), password, remove
	code, body := get(admin, "/users/"+fid(edID))
	if code != 200 {
		t.Fatalf("user page: %d", code)
	}
	has("admin", "/users/ed", body, "Rights and password", "Select all", "Remove user", `name="podcast"`, "May edit these podcasts")
	if strings.Index(body, "Alpha Show") > strings.Index(body, "Zed Show") {
		t.Error("podcasts on the user's page are not alphabetical")
	}
	// "Select all" shows everything ticked (not saved), and then offers "Select none"
	_, body = get(admin, "/users/"+fid(edID)+"?preset=all")
	if strings.Count(body, `name="podcast"`) != 2 || strings.Count(body, "checked") < 2 || !strings.Contains(body, "Select none") || !strings.Contains(body, "Not saved yet") {
		t.Error("?preset=all doesn't tick every podcast and offer 'Select none'")
	}
	if u, _, _ := st.userByQuery(`id=?`, edID); len(u.Podcasts) != 1 {
		t.Errorf("?preset=all saved something: %+v", u.Podcasts)
	}

	// the podcast page: admins get all four settings tabs, an editor only People + Spelling fixes
	_, body = get(admin, "/feeds/"+fid(f1))
	has("admin", "/feeds/1", body, "Podcast settings", `href="#general"`, `href="#feeds"`, `href="#people"`, `href="#spelling"`, `id="remove"`)
	_, body = get(editor, "/feeds/"+fid(f1))
	has("editor", "/feeds/1", body, "Podcast settings", `href="#people"`, `href="#spelling"`)
	if strings.Contains(body, `href="#general"`) || strings.Contains(body, `id="remove"`) {
		t.Error("an editor sees the admin tabs of the podcast settings")
	}
	// a podcast the editor may not edit: no settings panel at all
	_, body = get(editor, "/feeds/"+fid(f2))
	has("editor", "/feeds/2", body, "Alpha Show")
	if strings.Contains(body, "Podcast settings") {
		t.Error("an editor sees podcast settings of a podcast they may not edit")
	}

	// the rest that was touched
	for _, p := range []string{"/help", "/start", "/search", "/", "/account", "/users/activity", "/work"} {
		for who, c := range map[string]*http.Client{"admin": admin, "editor": editor} {
			code, body := get(c, p)
			if (p == "/users/activity" || p == "/work") && who == "editor" {
				continue // admin pages
			}
			if code != 200 {
				t.Errorf("%s %s: status %d", who, p, code)
				continue
			}
			has(who, p, body)
		}
	}
}
