package main

// Meta tags for search engines and link previews (Discord, Slack, Mastodon,
// WhatsApp, ... read Open Graph; X reads twitter:*). Podcast and episode
// pages show the podcast's artwork, everything else the QS-PodScript banner
// (/static/og.png). Only the public pages of a server are meant for search
// engines; the local app and everything behind a login say "noindex".

import (
	"fmt"
	"net/http"
)

const siteName = "QS-PodScript"

// toolDescription: what QS-PodScript is - the description on every page
// (search results, link previews)
const toolDescription = "QS-PodScript turns podcast episodes into transcripts that show who said what: " +
	"speaker detection, voice recognition, full-text search and the audio to listen along. Free and open source."

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
		Description: toolDescription,
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
	// the description is about QS-PodScript itself, the same on every page;
	// title and picture say which podcast / episode a link is about
	switch page {
	case "home":
		m.Title = siteName + " – podcast transcripts"
	case "feed":
		if f, ok := d["Feed"].(Feed); ok {
			art(f)
		}
	case "episode":
		ep, ok1 := d["Episode"].(Episode)
		f, ok2 := d["Feed"].(Feed)
		if ok1 && ok2 {
			art(f)
			m.Type = "article"
			m.Title = ep.Title + " – " + f.Title
			if _, ok := d["Version"]; !ok { // not transcribed yet: nothing to find
				m.Index = false
			}
		}
	}
	m.DocTitle = m.Title + " – " + siteName
	if page == "home" {
		m.DocTitle = m.Title
	}
	return m
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
