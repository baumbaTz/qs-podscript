package main

// Podcast artwork: the image a feed names (<itunes:image href> or
// <image><url>) is downloaded once, made small (300 px, JPEG) and kept in
// data/images, so pages load it from here - quick, and visitors' browsers
// don't contact the podcast's host. A background job looks for podcasts
// without artwork at the start and after changes, and checks every podcast
// again once a week (shows change their art).

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	artSize      = 300
	artRecheck   = 7 * 24 * time.Hour
	artRetryFail = 24 * time.Hour
)

var artKick = make(chan struct{}, 1)

func artworkKick() {
	select {
	case artKick <- struct{}{}:
	default:
	}
}

func runArtwork(ctx context.Context, st *Store) {
	for {
		updateArtwork(ctx, st)
		select {
		case <-ctx.Done():
			return
		case <-artKick:
			time.Sleep(2 * time.Second)
		case <-time.After(6 * time.Hour):
		}
	}
}

func updateArtwork(ctx context.Context, st *Store) {
	rows, err := st.db.Query(`SELECT id, url, image_url, image_file, image_checked FROM feeds`)
	if err != nil {
		return
	}
	type row struct {
		id             int64
		url, img, file string
		checked        int64
	}
	var todo []row
	now := time.Now()
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.url, &r.img, &r.file, &r.checked) != nil {
			continue
		}
		age := now.Sub(time.Unix(r.checked, 0))
		if (r.file == "" && age > artRetryFail) || age > artRecheck || r.checked == 0 {
			todo = append(todo, r)
		}
	}
	rows.Close()
	for _, r := range todo {
		if ctx.Err() != nil {
			return
		}
		img, err := feedImageURL(ctx, r.url)
		if err == nil && img != "" && (img != r.img || r.file == "" || !fileExists(filepath.Join(P.Images, r.file))) {
			var name string
			if name, err = saveArtwork(ctx, r.id, img); err == nil {
				st.db.Exec(`UPDATE feeds SET image_url=?, image_file=? WHERE id=?`, img, name, r.id)
				if r.file != "" && r.file != name {
					os.Remove(filepath.Join(P.Images, r.file))
				}
				debugf("artwork for podcast %d: %s", r.id, name)
			}
		}
		if err != nil {
			debugf("artwork for podcast %d: %v", r.id, err)
		}
		st.db.Exec(`UPDATE feeds SET image_checked=? WHERE id=?`, now.Unix(), r.id)
	}
}

// removeArtwork: the podcast is gone.
func removeArtwork(file string) {
	if file != "" && file == filepath.Base(file) {
		os.Remove(filepath.Join(P.Images, file))
	}
}

// feedImageURL reads the channel's artwork address from the feed.
func feedImageURL(ctx context.Context, feedURL string) (string, error) {
	body, err := httpGetLimited(ctx, feedURL, 200<<20)
	if err != nil {
		return "", err
	}
	return parseFeedImage(body), nil
}

func parseFeedImage(body []byte) string {
	var doc struct {
		Channel struct {
			Itunes struct {
				Href string `xml:"href,attr"`
			} `xml:"http://www.itunes.com/dtds/podcast-1.0.dtd image"`
			Image struct {
				URL string `xml:"url"`
			} `xml:"image"`
		} `xml:"channel"`
	}
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	dec.CharsetReader = charsetReader
	if dec.Decode(&doc) != nil {
		return ""
	}
	for _, u := range []string{doc.Channel.Itunes.Href, doc.Channel.Image.URL} {
		u = strings.TrimSpace(u)
		if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
			return u
		}
	}
	return ""
}

func httpGetLimited(ctx context.Context, u string, max int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("larger than %d MB", max>>20)
	}
	return b, nil
}

// saveArtwork downloads, shrinks and stores the image; returns the file name
// (it changes with the content, so browsers may keep it for good).
func saveArtwork(ctx context.Context, feedID int64, u string) (string, error) {
	b, err := httpGetLimited(ctx, u, 25<<20)
	if err != nil {
		return "", err
	}
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return "", fmt.Errorf("not a JPEG/PNG/GIF image: %v", err)
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, shrinkImage(src, artSize), &jpeg.Options{Quality: 85}); err != nil {
		return "", err
	}
	name := fmt.Sprintf("podcast-%d-%08x.jpg", feedID, crc32.ChecksumIEEE(out.Bytes()))
	tmp := filepath.Join(P.Images, name+".part")
	if err := os.WriteFile(tmp, out.Bytes(), 0o644); err != nil {
		return "", err
	}
	return name, os.Rename(tmp, filepath.Join(P.Images, name))
}

// shrinkImage scales to fit max x max by averaging (box filter) - good
// enough for artwork, no extra library needed. Transparent parts become
// white (JPEG has no transparency).
func shrinkImage(src image.Image, max int) image.Image {
	sb := src.Bounds()
	w, h := sb.Dx(), sb.Dy()
	if w <= 0 || h <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	nw, nh := w, h
	if w > max || h > max {
		if w >= h {
			nw, nh = max, h*max/w
		} else {
			nw, nh = w*max/h, max
		}
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0, y1 := sb.Min.Y+y*h/nh, sb.Min.Y+(y+1)*h/nh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < nw; x++ {
			x0, x1 := sb.Min.X+x*w/nw, sb.Min.X+(x+1)*w/nw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, b, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					cr, cg, cb, ca := src.At(xx, yy).RGBA()
					// on white: c + (1-a)
					r += uint64(cr + (0xffff - ca))
					g += uint64(cg + (0xffff - ca))
					b += uint64(cb + (0xffff - ca))
					n++
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(b / n >> 8), 255})
		}
	}
	return dst
}

// handleImage serves a stored artwork file (GET /img/{file}).
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if name != filepath.Base(name) || !strings.HasPrefix(name, "podcast-") || !strings.HasSuffix(name, ".jpg") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // the name changes with the picture
	http.ServeFile(w, r, filepath.Join(P.Images, name))
}
