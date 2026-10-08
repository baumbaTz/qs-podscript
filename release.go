package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The "Get started" page (/start) sends new helpers to the newest build for
// their system. The server asks GitHub which release is the newest and links
// the files of that release directly. The answer is cached (a visitor never
// waits for GitHub twice in six hours); when GitHub can't be reached the links
// are built from this server's own version instead – the same files, just not
// necessarily the newest ones.

const repoSlug = "baumbatz/qs-podscript"

var githubAPI = "https://api.github.com" // a variable so the tests can point it elsewhere

type ghRelease struct {
	Tag        string    `json:"tag_name"`
	HTMLURL    string    `json:"html_url"`
	Published  time.Time `json:"published_at"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// Download is one button on the page.
type Download struct {
	OS, Detail, File, URL string
	MB                    int
}

// the packages build.sh makes, in the order they are shown
var platforms = []struct{ OS, Detail, Suffix string }{
	{"Windows", "64-bit", "-windows-x64.zip"},
	{"Linux", "64-bit · Ubuntu 22.04+, Debian 12+", "-linux-x64.tar.gz"},
	{"macOS", "Apple Silicon · new, little tested", "-macos-arm64.zip"},
}

// app releases are tagged v1.2.3 – the repository also has releases that only
// carry dependencies (deps-whisper-vulkan-…), which must never be offered
var appTag = regexp.MustCompile(`^v[0-9]+(\.[0-9]+)+$`)

const (
	releaseTTL   = 6 * time.Hour
	releaseRetry = 10 * time.Minute
)

var relCache struct {
	sync.Mutex
	rel *ghRelease
	at  time.Time // of the last attempt
	ok  bool      // did it work
}

// latestRelease returns the newest app release on GitHub, nil if unknown. After
// a failed attempt the older answer (if any) is kept and the next try is
// ten minutes away.
func latestRelease() *ghRelease {
	relCache.Lock()
	defer relCache.Unlock()
	ttl := releaseRetry
	if relCache.ok {
		ttl = releaseTTL
	}
	if !relCache.at.IsZero() && time.Since(relCache.at) < ttl {
		return relCache.rel
	}
	relCache.at = time.Now()
	rel, err := fetchLatestRelease()
	if err != nil {
		relCache.ok = false
		logf("Newest release on GitHub: %v", err)
		return relCache.rel
	}
	relCache.rel, relCache.ok = rel, true
	return rel
}

func fetchLatestRelease() (*ghRelease, error) {
	req, err := http.NewRequest("GET", githubAPI+"/repos/"+repoSlug+"/releases?per_page=20", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "qs-podscript/"+version)
	resp, err := (&http.Client{Timeout: 4 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var all []ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&all); err != nil {
		return nil, err
	}
	for i := range all { // newest first
		r := &all[i]
		if r.Draft || r.Prerelease || !appTag.MatchString(r.Tag) {
			continue
		}
		return r, nil
	}
	return nil, fmt.Errorf("no app release found")
}

// downloadsFor lists the buttons for a release; without one (GitHub not
// reachable) they point at the files of this server's own version.
func downloadsFor(rel *ghRelease, ownVersion string) (tag string, list []Download, notes string) {
	if rel != nil {
		for _, p := range platforms {
			for _, a := range rel.Assets {
				if strings.HasSuffix(a.Name, p.Suffix) {
					list = append(list, Download{p.OS, p.Detail, a.Name, a.URL, int((a.Size + 500_000) / 1_000_000)})
					break
				}
			}
		}
		if len(list) > 0 {
			return rel.Tag, list, rel.HTMLURL
		}
	}
	tag = "v" + ownVersion
	base := "https://github.com/" + repoSlug + "/releases/download/" + tag + "/qs-podscript-" + ownVersion
	for _, p := range platforms {
		list = append(list, Download{p.OS, p.Detail, "qs-podscript-" + ownVersion + p.Suffix, base + p.Suffix, 0})
	}
	return tag, list, "https://github.com/" + repoSlug + "/releases/tag/" + tag
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	rel := latestRelease()
	tag, list, notes := downloadsFor(rel, version)
	s.render(w, r, "start", "Get started", "", map[string]any{
		"Tag": tag, "Downloads": list, "NotesURL": notes,
		"FromGitHub": rel != nil && notes == rel.HTMLURL,
		"Differs":    tag != "v"+version,
		"Origin":     origin(r),
		"Releases":   "https://github.com/" + repoSlug + "/releases",
		"Repo":       "https://github.com/" + repoSlug,
	})
}
