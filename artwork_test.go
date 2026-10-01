package main

import (
	"image"
	"image/color"
	"testing"
)

func TestFeedImage(t *testing.T) {
	both := []byte(`<?xml version="1.0"?><rss xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"><channel>
	<title>X</title><image><url>https://a.example/small.png</url></image>
	<itunes:image href="https://a.example/big.jpg"/><item><title>e</title></item></channel></rss>`)
	if u := parseFeedImage(both); u != "https://a.example/big.jpg" {
		t.Errorf("itunes image first: %q", u)
	}
	plain := []byte(`<rss><channel><image><url> https://a.example/small.png </url></image></channel></rss>`)
	if u := parseFeedImage(plain); u != "https://a.example/small.png" {
		t.Errorf("plain image: %q", u)
	}
	if u := parseFeedImage([]byte(`<rss><channel><image><url>javascript:x</url></image></channel></rss>`)); u != "" {
		t.Errorf("only http(s): %q", u)
	}
	// 1000x500 -> 300x150, colours kept, transparency on white
	src := image.NewNRGBA(image.Rect(0, 0, 1000, 500))
	for y := 0; y < 500; y++ {
		for x := 0; x < 1000; x++ {
			if x < 500 {
				src.Set(x, y, color.NRGBA{200, 0, 0, 255})
			} else {
				src.Set(x, y, color.NRGBA{0, 0, 0, 0})
			}
		}
	}
	out := shrinkImage(src, 300)
	if b := out.Bounds(); b.Dx() != 300 || b.Dy() != 150 {
		t.Fatalf("size %v", b)
	}
	if r, g, _, _ := out.At(10, 10).RGBA(); r>>8 < 190 || g>>8 > 10 {
		t.Errorf("red side: %d %d", r>>8, g>>8)
	}
	if r, g, b, _ := out.At(290, 10).RGBA(); r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Errorf("transparent side should be white: %d %d %d", r>>8, g>>8, b>>8)
	}
}
