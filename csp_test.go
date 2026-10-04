package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"runtime"
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
	r := httptest.NewRequest("GET", "/episodes/1?hl=x", nil)
	r.Host = "transcribe.example.org"
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := origin(r); got != "https://transcribe.example.org" {
		t.Errorf("origin: %q", got)
	}
}

func TestConnectNeedsUser(t *testing.T) {
	for _, args := range [][]string{nil, {"https://x.example.org"}, {"--user", "a"}} {
		if err := cmdConnect(args); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Errorf("connect %v: want usage error, got %v", args, err)
		}
	}
}

// gpuspeakers.go downloads sherpa-onnx's GPU build; it must be the same version
// as the Go binding (the C API must match).
func TestGPUSherpaVersionMatchesGoMod(t *testing.T) {
	mod, _ := os.ReadFile("go.mod")
	m := regexp.MustCompile(`k2-fsa/sherpa-onnx-go v(\S+)`).FindSubmatch(mod)
	if m == nil {
		t.Fatal("sherpa-onnx-go not found in go.mod")
	}
	if string(m[1]) != sherpaGPUVersion {
		t.Errorf("sherpaGPUVersion %s, go.mod sherpa-onnx-go %s – update the GPU downloads in gpuspeakers.go (names + sha256) too", sherpaGPUVersion, m[1])
	}
	for goos, p := range gpuSpeakerPkgs {
		if !strings.Contains(p.Archive.URL, p.Name) || len(p.Archive.SHA256) != 64 {
			t.Errorf("%s: archive URL/sha256 doesn't fit %s", goos, p.Name)
		}
		for _, w := range p.Wheels {
			if len(w.SHA256) != 64 || !strings.HasPrefix(w.URL, "https://") {
				t.Errorf("%s: bad wheel %s", goos, w.URL)
			}
		}
	}
}

func TestNvidiaLibFilter(t *testing.T) {
	cases := map[string]string{
		"nvidia/cu13/lib/libcudart.so.13":         "libcudart.so.13",
		"nvidia/cu13/lib/libnvblas.so.13":         "",
		"nvidia/cu13/include/cuda.h":              "",
		"nvidia_cublas-13.8.0.4.dist-info/RECORD": "",
	}
	if runtime.GOOS == "windows" {
		cases = map[string]string{"nvidia/cu13/bin/x86_64/cudart64_13.dll": "cudart64_13.dll", "nvidia/cu13/lib/x64/cudart.lib": ""}
	}
	for in, want := range cases {
		if got := nvidiaLibFilter(in); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}

func TestComputeCapFromName(t *testing.T) {
	for name, want := range map[string]float64{
		"NVIDIA GeForce RTX 3070": 7.5, "NVIDIA GeForce GTX 1660 SUPER": 7.5,
		"NVIDIA GeForce GT 1030": 6.1, "NVIDIA GeForce GTX 1080 Ti": 6.1,
		"NVIDIA GeForce GTX 970": 5.2, "NVIDIA TITAN V": 7.0,
	} {
		if got, ok := computeCapFromName(name); !ok || got != want {
			t.Errorf("%s: got %v %v, want %v", name, got, ok, want)
		}
	}
	if _, ok := computeCapFromName("Some future card"); ok {
		t.Error("unknown name should be unclear")
	}
}
