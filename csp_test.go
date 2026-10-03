package main

import (
	"io/fs"
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
