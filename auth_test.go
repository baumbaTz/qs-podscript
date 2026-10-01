package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPasswordsAndSessions(t *testing.T) {
	h, err := hashPassword("correct horse")
	if err != nil || !checkPassword(h, "correct horse") || checkPassword(h, "correct horsE") || checkPassword("garbage", "x") {
		t.Fatalf("password check broken: %v", err)
	}
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	defer func() { P.DB = old }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser("a", "short", roleEditor); err == nil {
		t.Fatal("short password accepted")
	}
	id, err := st.AddUser("Helper", "longenough1", roleEditor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser("helper", "longenough1", roleEditor); err == nil {
		t.Fatal("duplicate name (other case) accepted")
	}
	f, _ := st.AddFeed(Feed{URL: "https://x/f.xml", Title: "F"})
	st.SetUserPodcasts(id, []int64{f})
	tok, _ := st.NewSession(id)
	u := st.SessionUser(tok)
	if u == nil || u.Name != "Helper" || !u.Podcasts[f] || u.IsAdmin() {
		t.Fatalf("session user %+v", u)
	}
	st.SetPassword(id, "anotherpass1") // logs out everywhere
	if st.SessionUser(tok) != nil {
		t.Fatal("session survived password change")
	}
	if safeNext("//evil.com") != "/" || safeNext("/feeds/1") != "/feeds/1" || safeNext("https://x") != "/" {
		t.Fatal("safeNext")
	}
}

func TestClientIPTrustedProxy(t *testing.T) {
	req := func(remote, xff string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	setTrustedProxies("")
	if got := clientIP(req("10.0.0.5:4000", "1.2.3.4")); got != "10.0.0.5" {
		t.Fatalf("untrusted proxy believed: %s", got)
	}
	if got := clientIP(req("127.0.0.1:4000", "9.9.9.9, 1.2.3.4")); got != "1.2.3.4" {
		t.Fatalf("loopback: %s", got)
	}
	if err := setTrustedProxies("10.0.0.0/24, 192.168.1.7"); err != nil {
		t.Fatal(err)
	}
	defer setTrustedProxies("")
	if got := clientIP(req("10.0.0.5:4000", "1.2.3.4")); got != "1.2.3.4" {
		t.Fatalf("trusted net: %s", got)
	}
	if got := clientIP(req("192.168.1.7:4000", "5.6.7.8")); got != "5.6.7.8" {
		t.Fatalf("trusted host: %s", got)
	}
	if got := clientIP(req("192.168.1.8:4000", "5.6.7.8")); got != "192.168.1.8" {
		t.Fatalf("other host believed: %s", got)
	}
	if setTrustedProxies("nonsense") == nil {
		t.Fatal("bad address accepted")
	}
}
