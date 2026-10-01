package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type FeedItem struct {
	GUID      string
	Title     string
	PubDate   int64
	AudioURL  string
	DurationS int
}

type rssDoc struct {
	Channel struct {
		Title string    `xml:"title"`
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title     string `xml:"title"`
	GUID      string `xml:"guid"`
	PubDate   string `xml:"pubDate"`
	Enclosure struct {
		URL  string `xml:"url,attr"`
		Type string `xml:"type,attr"`
	} `xml:"enclosure"`
	Duration string `xml:"http://www.itunes.com/dtds/podcast-1.0.dtd duration"`
}

const userAgent = "QS-PodScript/" + version

func fetchFeed(ctx context.Context, url string) (string, []FeedItem, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", nil, fmt.Errorf("feed returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 200<<20))
	if err != nil {
		return "", nil, err
	}
	return parseFeed(body)
}

func parseFeed(body []byte) (string, []FeedItem, error) {
	var doc rssDoc
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	dec.CharsetReader = charsetReader
	if err := dec.Decode(&doc); err != nil {
		return "", nil, fmt.Errorf("cannot parse feed XML: %w", err)
	}
	var items []FeedItem
	skipped := 0
	for _, it := range doc.Channel.Items {
		url := strings.TrimSpace(it.Enclosure.URL)
		if url == "" {
			skipped++
			continue
		}
		guid := strings.TrimSpace(it.GUID)
		if guid == "" {
			guid = url
		}
		pd, ok := parseRSSDate(it.PubDate)
		if !ok {
			debugf("unparsable pubDate %q for %q", it.PubDate, it.Title)
		}
		items = append(items, FeedItem{
			GUID:      guid,
			Title:     strings.TrimSpace(it.Title),
			PubDate:   pd,
			AudioURL:  url,
			DurationS: parseDuration(it.Duration),
		})
	}
	if skipped > 0 {
		debugf("feed: skipped %d items without audio enclosure", skipped)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].PubDate < items[j].PubDate })
	return strings.TrimSpace(doc.Channel.Title), items, nil
}

var rssDateFormats = []string{
	time.RFC1123Z,
	time.RFC1123,
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"Mon, 02 Jan 2006 15:04 -0700",
	"Mon, 2 Jan 2006 15:04 -0700",
	"2 Jan 2006 15:04:05 -0700",
	"02 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006",
	time.RFC3339,
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func parseRSSDate(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	for _, f := range rssDateFormats {
		if t, err := time.Parse(f, s); err == nil {
			return t.Unix(), true
		}
	}
	return 0, false
}

// parseDuration handles "HH:MM:SS", "MM:SS" and plain seconds.
func parseDuration(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	total := 0
	for _, p := range strings.Split(s, ":") {
		n, err := strconv.Atoi(strings.TrimSpace(strings.SplitN(p, ".", 2)[0]))
		if err != nil {
			return 0
		}
		total = total*60 + n
	}
	return total
}

// charsetReader handles the few non-UTF-8 encodings podcast feeds use.
// Windows-1252 is treated as Latin-1 (close enough for titles).
func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "utf-8", "utf8", "us-ascii", "ascii":
		return input, nil
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1", "windows-1252", "cp1252":
		b, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c)
		}
		return strings.NewReader(string(r)), nil
	}
	return nil, fmt.Errorf("unsupported feed charset %q", charset)
}
