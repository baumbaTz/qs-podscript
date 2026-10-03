package main

// Meta tags for search engines and link previews (Discord, Slack, Mastodon,
// WhatsApp, ... read Open Graph; X reads twitter:*). Podcast and episode
// pages show the podcast's artwork, everything else the QS-PodScript banner
// (/static/og.png). Only the public pages of a server are meant for search
// engines; the local app and everything behind a login say "noindex".

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const siteName = "QS-PodScript"

type pageMeta struct {
	Title       string // og:title (the page title, sometimes with more context)
	DocTitle    string // <title>: Title + " – QS-PodScript" (home: the site's own)
	Description string
	URL         string // canonical address (absolute)
	Image       string // absolute
	ImageAlt    string
	LargeImage  bool // banner (1200x630) instead of square artwork
	Type        string
	Index       bool // search engines may list this page
	ThemeColor  string
}

// origin: scheme and host the visitor used (the reverse proxy passes Host
// and X-Forwarded-Proto through).
func origin(r *http.Request) string {
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// indexable pages (server mode, public)
var indexPages = map[string]bool{"home": true, "feed": true, "episode": true, "help": true}

func (s *Server) buildMeta(r *http.Request, page, title, look string, data any) pageMeta {
	o := origin(r)
	m := pageMeta{
		Title:       title,
		URL:         o + r.URL.Path,
		Image:       o + "/static/og.png?v=" + version,
		ImageAlt:    "QS-PodScript – podcast transcripts, who said what",
		LargeImage:  true,
		Type:        "website",
		Index:       s.serverMode && indexPages[page],
		ThemeColor:  "#B3191D",
		Description: "Podcast transcripts that show who said what – searchable, with the audio to listen along.",
	}
	if look == lookClassic {
		m.ThemeColor = "#24508F"
	}
	d, _ := data.(map[string]any)
	art := func(f Feed) {
		if f.Image != "" {
			m.Image, m.ImageAlt, m.LargeImage = o+f.ImageURL(), f.Title, false
		}
	}
	switch page {
	case "home":
		m.Title = siteName + " – podcast transcripts"
		m.Description = s.homeDescription()
	case "feed":
		if f, ok := d["Feed"].(Feed); ok {
			art(f)
			c, _ := s.st.StatusCounts(f.ID)
			m.Description = fmt.Sprintf("Transcripts of %s: %s – who said what, searchable, with the audio to listen along.",
				f.Title, plural(c["done"], "episode", "episodes"))
		}
	case "episode":
		ep, ok1 := d["Episode"].(Episode)
		f, ok2 := d["Feed"].(Feed)
		if ok1 && ok2 {
			art(f)
			m.Type = "article"
			m.Title = ep.Title + " – " + f.Title
			when := ""
			if ep.PubDate > 0 {
				when = ", " + time.Unix(ep.PubDate, 0).UTC().Format("2 January 2006")
			}
			m.Description = fmt.Sprintf("Transcript of “%s” (%s%s) – who said what, searchable, with the audio.", ep.Title, f.Title, when)
			if us, ok := d["Utterances"].([]Utterance); ok {
				if q := firstWords(us, 160); q != "" {
					m.Description += " “" + q + "”"
				}
			}
			if _, ok := d["Version"]; !ok { // not transcribed yet: nothing to find
				m.Index = false
				m.Description = fmt.Sprintf("“%s” (%s%s) – not transcribed yet.", ep.Title, f.Title, when)
			}
		}
	case "search":
		if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
			m.Description = "Search results for “" + q + "” in the podcast transcripts."
		}
	case "help":
		m.Description = "How QS-PodScript works: transcripts with speaker names, search, fixing who said what."
	}
	m.DocTitle = m.Title + " – " + siteName
	if page == "home" {
		m.DocTitle = m.Title
	}
	return m
}

// homeDescription: "Searchable transcripts of Film Sack, GORE and 1 more
// podcast – who said what ... 1,234 episodes transcribed."
func (s *Server) homeDescription() string {
	fs := s.feedSummaries()
	var names []string
	done := 0
	for _, f := range fs {
		if f.Done > 0 {
			names = append(names, f.Title)
		}
		done += f.Done
	}
	if len(names) == 0 {
		return "Podcast transcripts that show who said what – searchable, with the audio to listen along."
	}
	list := names
	more := ""
	if len(names) > 3 {
		list = names[:3]
		more = " and " + plural(len(names)-3, "more podcast", "more podcasts")
	}
	show := strings.Join(list, ", ")
	if more == "" && len(list) > 1 {
		show = strings.Join(list[:len(list)-1], ", ") + " and " + list[len(list)-1]
	}
	return fmt.Sprintf("Searchable transcripts of %s%s – who said what, with the audio to listen along. %s transcribed.",
		show, more, plural(done, "episode", "episodes"))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return groupThousands(n) + " " + many
}

func groupThousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// firstWords: the beginning of the transcript (no ads), cut at a word.
func firstWords(us []Utterance, max int) string {
	var b strings.Builder
	for _, u := range us {
		if u.Mark != "" || strings.TrimSpace(u.Text) == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(strings.TrimSpace(u.Text))
		if b.Len() >= max {
			break
		}
	}
	t := strings.Join(strings.Fields(b.String()), " ")
	if utf8.RuneCountInString(t) <= max {
		return t
	}
	rs := []rune(t)[:max]
	cut := string(rs)
	if i := strings.LastIndexByte(cut, ' '); i > max/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, ",.;:–- ") + " …"
}

// ---------------------------------------------------------------- robots / sitemap / favicon

func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !s.serverMode { // the local app is nobody's business
		fmt.Fprint(w, "User-agent: *\nDisallow: /\n")
		return
	}
	fmt.Fprint(w, "User-agent: *\n")
	for _, p := range []string{"/audio/", "/api/", "/events", "/login", "/search", "/account", "/users",
		"/setup", "/queue", "/log", "/work", "/people", "/activity", "/settings", "/_local/"} {
		fmt.Fprintf(w, "Disallow: %s\n", p)
	}
	fmt.Fprintf(w, "\nSitemap: %s/sitemap.xml\n", origin(r))
}

// handleSitemap: the overview, the manual, every podcast and every
// transcribed episode (server mode only).
func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	if !s.serverMode {
		http.NotFound(w, r)
		return
	}
	o := origin(r)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`+"\n")
	loc := func(path string) { fmt.Fprintf(w, "<url><loc>%s%s</loc></url>\n", o, path) }
	loc("/")
	loc("/help")
	feeds, _ := s.st.Feeds()
	for _, f := range feeds {
		loc(fmt.Sprintf("/feeds/%d", f.ID))
		eps, _ := s.st.Episodes(f.ID, "", 0)
		for _, e := range eps {
			if e.ActiveVersionID.Valid {
				loc(fmt.Sprintf("/episodes/%d", e.ID))
			}
		}
	}
	fmt.Fprint(w, "</urlset>\n")
}

// /favicon.ico: browsers and link-preview bots ask for it at the root
func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	b, err := webFS.ReadFile("web/static/favicon.ico")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Write(b)
}
