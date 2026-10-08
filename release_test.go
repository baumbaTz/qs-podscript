package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func resetReleaseCache() {
	relCache.Lock()
	relCache.rel, relCache.at, relCache.ok = nil, time.Time{}, false
	relCache.Unlock()
}

// the GitHub answer: newest first, with the things that must never be offered
// (a dependency release, a draft, a prerelease) in front of the real one
const releasesJSON = `[
 {"tag_name":"deps-whisper-vulkan-1.9.2","html_url":"https://x/deps","draft":false,"prerelease":false,"assets":[{"name":"whisper-cli.zip","browser_download_url":"https://x/deps.zip","size":1}]},
 {"tag_name":"v9.9.9","html_url":"https://x/draft","draft":true,"prerelease":false,"assets":[]},
 {"tag_name":"v9.9.8","html_url":"https://x/pre","draft":false,"prerelease":true,"assets":[]},
 {"tag_name":"v0.40.0","html_url":"https://github.com/baumbatz/qs-podscript/releases/tag/v0.40.0","published_at":"2026-10-08T10:00:00Z","draft":false,"prerelease":false,"assets":[
   {"name":"qs-podscript-0.40.0-linux-x64.tar.gz","browser_download_url":"https://dl/linux.tar.gz","size":96400000},
   {"name":"qs-podscript-0.40.0-windows-x64.zip","browser_download_url":"https://dl/win.zip","size":131900000},
   {"name":"qs-podscript-0.40.0-macos-arm64.zip","browser_download_url":"https://dl/mac.zip","size":88000000},
   {"name":"qs-podscript-0.40.0-src.zip","browser_download_url":"https://dl/src.zip","size":2000000},
   {"name":"SHA256SUMS.txt","browser_download_url":"https://dl/sums","size":400}]},
 {"tag_name":"v0.39.0","html_url":"https://x/old","draft":false,"prerelease":false,"assets":[]}
]`

func TestLatestReleaseFromGitHub(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/repos/"+repoSlug+"/releases" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("GitHub requires a User-Agent")
		}
		fmt.Fprint(w, releasesJSON)
	}))
	defer srv.Close()
	old := githubAPI
	githubAPI = srv.URL
	defer func() { githubAPI = old; resetReleaseCache() }()
	resetReleaseCache()

	rel := latestRelease()
	if rel == nil || rel.Tag != "v0.40.0" {
		t.Fatalf("newest app release = %+v, want v0.40.0 (not the deps release, draft or prerelease)", rel)
	}
	tag, list, notes := downloadsFor(rel, "0.38.0")
	if tag != "v0.40.0" || !strings.HasSuffix(notes, "/releases/tag/v0.40.0") {
		t.Fatalf("tag %q notes %q", tag, notes)
	}
	want := []Download{
		{"Windows", "64-bit", "qs-podscript-0.40.0-windows-x64.zip", "https://dl/win.zip", 132},
		{"Linux", "64-bit · Ubuntu 22.04+, Debian 12+", "qs-podscript-0.40.0-linux-x64.tar.gz", "https://dl/linux.tar.gz", 96},
		{"macOS", "Apple Silicon · new, little tested", "qs-podscript-0.40.0-macos-arm64.zip", "https://dl/mac.zip", 88},
	}
	if len(list) != len(want) {
		t.Fatalf("got %d downloads, want %d: %+v", len(list), len(want), list)
	}
	for i := range want {
		if list[i] != want[i] {
			t.Errorf("download %d = %+v, want %+v", i, list[i], want[i])
		}
	}

	// cached: asking again does not bother GitHub
	latestRelease()
	latestRelease()
	if hits.Load() != 1 {
		t.Fatalf("GitHub was asked %d times, want 1 (cached)", hits.Load())
	}

	// GitHub fails later: the older answer is kept, and retried only after a pause
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "rate limited", http.StatusForbidden)
	})
	relCache.Lock()
	relCache.at = time.Now().Add(-releaseTTL - time.Minute)
	relCache.Unlock()
	if got := latestRelease(); got == nil || got.Tag != "v0.40.0" {
		t.Fatalf("after a failed refresh the old release should stay, got %+v", got)
	}
	n := hits.Load()
	latestRelease()
	if hits.Load() != n {
		t.Fatal("a failed lookup was retried at once instead of after the pause")
	}
}

func TestDownloadsWithoutGitHub(t *testing.T) {
	old := githubAPI
	githubAPI = "http://127.0.0.1:1" // nothing listens there
	defer func() { githubAPI = old; resetReleaseCache() }()
	resetReleaseCache()
	if rel := latestRelease(); rel != nil {
		t.Fatalf("expected no release, got %+v", rel)
	}
	tag, list, notes := downloadsFor(nil, "0.38.0")
	if tag != "v0.38.0" || notes != "https://github.com/baumbatz/qs-podscript/releases/tag/v0.38.0" {
		t.Fatalf("tag %q notes %q", tag, notes)
	}
	if len(list) != 3 || list[0].URL != "https://github.com/baumbatz/qs-podscript/releases/download/v0.38.0/qs-podscript-0.38.0-windows-x64.zip" ||
		list[1].URL != "https://github.com/baumbatz/qs-podscript/releases/download/v0.38.0/qs-podscript-0.38.0-linux-x64.tar.gz" ||
		list[2].URL != "https://github.com/baumbatz/qs-podscript/releases/download/v0.38.0/qs-podscript-0.38.0-macos-arm64.zip" {
		t.Fatalf("fallback links wrong: %+v", list)
	}
	// a release that has none of the packages (still being built) falls back as well
	tag, list, _ = downloadsFor(&ghRelease{Tag: "v0.41.0"}, "0.38.0")
	if tag != "v0.38.0" || len(list) != 3 {
		t.Fatalf("release without packages: %q %d", tag, len(list))
	}
}
