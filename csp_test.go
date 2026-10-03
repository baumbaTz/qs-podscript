package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The pages must work under a strict Content-Security-Policy
// (style-src 'self'; script-src 'self'): no inline scripts, no style
// attributes or <style> blocks, no on...= handlers, no javascript: links.
func TestTemplatesCSPClean(t *testing.T) {
	bad := []*regexp.Regexp{
		regexp.MustCompile(`<script(\s[^>]*)?>[^<\s]`),                             // inline script body
		regexp.MustCompile(`<script(?:\s+(?:type|defer|async)(?:="[^"]*")?)*\s*>`), // <script> without src
		regexp.MustCompile(`\sstyle="`),
		regexp.MustCompile(`<style[\s>]`),
		regexp.MustCompile(`\son[a-z]+="`),
		regexp.MustCompile(`(?i)href="javascript:`),
	}
	n := 0
	err := fs.WalkDir(webFS, "web/templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		b, err := webFS.ReadFile(path)
		if err != nil {
			return err
		}
		n++
		for i, line := range strings.Split(string(b), "\n") {
			for _, re := range bad {
				if re.MatchString(line) {
					t.Errorf("%s:%d: not allowed under a strict CSP (%s): %s", path, i+1, re, strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no templates found")
	}
}

func TestBarClass(t *testing.T) {
	f := tmplFuncs["barClass"].(func(any) string)
	for _, c := range []struct {
		in   any
		want string
	}{{0, "p0"}, {37, "p37"}, {-1, "p0"}, {250, "p100"}, {49.6, "p50"}, {int64(12), "p12"}, {"x", "p0"}} {
		if got := f(c.in); got != c.want {
			t.Errorf("barClass(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestStaticCaching(t *testing.T) {
	h := staticFiles(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for path, want := range map[string]string{
		"/static/app.css?v=0.31.2":                    "public, max-age=31536000, immutable",
		"/static/fonts/oswald-latin-500-normal.woff2": "public, max-age=31536000, immutable",
		"/static/app.css":                             "public, max-age=300",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if got := w.Header().Get("Cache-Control"); got != want {
			t.Errorf("%s: Cache-Control %q, want %q", path, got, want)
		}
	}
}

func TestMetaHelpers(t *testing.T) {
	if got := plural(1234, "episode", "episodes"); got != "1,234 episodes" {
		t.Errorf("plural: %q", got)
	}
	if got := plural(1, "episode", "episodes"); got != "1 episode" {
		t.Errorf("plural: %q", got)
	}
	us := []Utterance{{Text: "Buy socks.", Mark: "ad"}, {Text: "Welcome back to the show, everybody"}, {Text: "and today we talk about a very long film indeed"}}
	if got := firstWords(us, 40); got != "Welcome back to the show, everybody and …" {
		t.Errorf("firstWords: %q", got)
	}
	r := httptest.NewRequest("GET", "/episodes/1?hl=x", nil)
	r.Host = "transcribe.example.org"
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := origin(r); got != "https://transcribe.example.org" {
		t.Errorf("origin: %q", got)
	}
}
