package main

// Demo data for the screenshots in docs/screenshots (not a real test):
//   QSPS_DEMO_HOME=/tmp/demo go test -run TestMakeDemo -tags sqlite_fts5 .
// then: QSPODSCRIPT_HOME=/tmp/demo ./qs-podscript serve
// A made-up podcast with three people, a transcribed episode with an ad and a
// movie clip, some checked passages and silent audio of the right length.

import (
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type demoLine struct {
	who  int // 0,1,2 = people, 3 = unnamed guest voice
	text string
	mark string
}

var demoScript = []demoLine{
	{0, "Welcome back to The Late Night Film Club, the podcast where we watch the movies so you don't have to. I'm Alex.", ""},
	{1, "And I'm Sam. This week it's a cold one: John Carpenter's The Thing from 1982.", ""},
	{2, "And I brought the snacks, which feels wrong for a movie about something that eats people from the inside.", ""},
	{0, "Jordan is back! Before we get to the dogs, a word from the people who keep the lights on.", ""},
	{0, "This episode is brought to you by Cozy Socks. Warm feet, Antarctic nights, use the code FILMCLUB for ten percent off.", "ad"},
	{1, "Okay. So the opening shot. A helicopter chasing a dog across the snow, and nobody tells you why.", ""},
	{2, "That's the genius of it. You're on the dog's side for about four minutes.", ""},
	{3, "Nobody trusts anybody right now, and I'm tired.", "clip"},
	{0, "Kurt Russell in the hat. Best hat in movie history, I will not take questions.", ""},
	{1, "The blood test scene is still the most tense thing I've ever watched. Every single time.", ""},
	{2, "Because the practical effects hold up. Rob Bottin was twenty-two when he did those creatures.", ""},
	{0, "Twenty-two! I was twenty-two and I couldn't keep a cactus alive.", ""},
	{1, "And the ending. Two men, a fire, and absolutely no answers. Who's the Thing?", ""},
	{2, "Neither. Both. It doesn't matter. That's the point.", ""},
	{0, "That's going to start a fight in the comments. Rating time: I give it five frozen flamethrowers.", ""},
	{1, "Five out of five, no notes.", ""},
	{2, "Four and a half. I'm docking half a point for what happened to the dogs.", ""},
	{0, "Fair. Next week: Alien. Thanks for listening, and check your friends for tentacles.", ""},
}

func TestMakeDemo(t *testing.T) {
	home := os.Getenv("QSPS_DEMO_HOME")
	if home == "" {
		t.Skip("set QSPS_DEMO_HOME")
	}
	os.RemoveAll(filepath.Join(home, "data"))
	os.Setenv("QSPODSCRIPT_HOME", home)
	if err := initPaths(); err != nil {
		t.Fatal(err)
	}
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.SetSetting("setup_check", "ok")

	f, _ := st.AddFeed(Feed{URL: "https://example.org/latenightfilmclub/feed.xml", Title: "The Late Night Film Club", Language: "en"})
	srcs, _ := st.Sources(f)
	titles := []string{"The Thing (1982)", "Alien (1979)", "Jaws (1975)", "Tremors (1990)", "The Fly (1986)", "Predator (1987)"}
	var items []FeedItem
	for i, ti := range titles {
		items = append(items, FeedItem{GUID: fmt.Sprint("ep", i), Title: fmt.Sprintf("Episode %d: %s", 112+i, ti),
			PubDate: time.Date(2026, 6, 1+7*i, 6, 0, 0, 0, time.UTC).Unix(), AudioURL: fmt.Sprintf("https://example.org/ep%d.mp3", i)})
	}
	st.UpsertEpisodes(f, srcs[0].ID, items)
	writeDemoArt(t, f, color.RGBA{24, 32, 48, 255}, color.RGBA{220, 60, 50, 255})

	names := []string{"Alex Morgan", "Sam Rivera", "Jordan Lee"}
	var ids []int64
	for _, n := range names {
		id, _ := st.AddPerson(n)
		ids = append(ids, id)
	}
	st.SetRoster(f, map[int64]string{ids[0]: roleHost, ids[1]: roleHost, ids[2]: rolePool})

	eps, _ := st.Episodes(f, "", 20)
	for _, ep := range eps {
		if !strings.Contains(ep.Title, "Thing") && !strings.Contains(ep.Title, "Alien") && !strings.Contains(ep.Title, "Jaws") {
			continue
		}
		vid, _ := st.CreateVersion(Version{EpisodeID: ep.ID, WhisperModel: "turbo", WhisperBackend: "cuda", Language: "en"})
		var segs []Segment
		var toks []Token
		var turns []Turn
		ms := int64(0)
		for i, l := range demoScript {
			start := ms
			for j, w := range strings.Fields(l.text) {
				d := int64(180 + 35*len(w))
				p := 0.95
				if w == "Bottin" || w == "FILMCLUB" {
					p = 0.4 // shows the "not sure" underline
				}
				toks = append(toks, Token{SegIdx: i, Idx: j, StartMs: ms, EndMs: ms + d, Text: " " + w, P: p})
				ms += d + 40
			}
			segs = append(segs, Segment{Idx: i, StartMs: start, EndMs: ms, Text: l.text})
			turns = append(turns, Turn{StartMs: start, EndMs: ms, Cluster: l.who})
			ms += 350
		}
		if err := st.SaveTranscription(vid, segs, toks); err != nil {
			t.Fatal(err)
		}
		st.SaveDiarization(vid, turns, nil)
		audio := fmt.Sprintf("ep%d_v%d.ogg", ep.ID, vid)
		out := filepath.Join(P.Audio, audio)
		if b, err := exec.Command("ffmpeg", "-y", "-loglevel", "error", "-f", "lavfi", "-i", "anullsrc=r=16000:cl=mono",
			"-t", fmt.Sprintf("%.1f", float64(ms)/1000), "-c:a", "libopus", "-b:a", "16k", out).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v %s", err, b)
		}
		st.FinishVersion(Version{ID: vid, EpisodeID: ep.ID, Status: "done", AudioFile: audio, AudioSeconds: float64(ms) / 1000,
			NumClusters: 4, DiarizeInfo: "titanet-large, step 2.5s, threshold 0.96"})
		st.FinishEpisode(ep.ID, vid)
		for k, id := range ids {
			st.AddCorrection(vid, Correction{Kind: "merge", From: k, Label: personLabel(id), CreatedAt: time.Now().Unix()})
		}
		for _, s := range segs {
			if m := demoScript[s.Idx].mark; m != "" {
				st.AddCorrection(vid, Correction{Kind: "mark", StartMs: s.StartMs, EndMs: s.EndMs, Text: m, CreatedAt: time.Now().Unix()})
			}
		}
		// the first half is checked by someone
		st.db.Exec(`INSERT INTO checks(version_id,start_ms,end_ms,user_id,changed,created_at) VALUES(?,?,?,?,?,?)`, vid, 0, ms/2, 0, 1, time.Now().Unix())
	}
	st.db.Exec(`UPDATE episodes SET status='queued' WHERE active_version_id IS NULL AND title LIKE '%Tremors%'`)
	// a second podcast for the list
	f2, _ := st.AddFeed(Feed{URL: "https://example.org/retrogames/feed.xml", Title: "Retro Games Weekly", Language: "en"})
	s2, _ := st.Sources(f2)
	st.UpsertEpisodes(f2, s2[0].ID, []FeedItem{{GUID: "r1", Title: "Episode 1", PubDate: 1, AudioURL: "https://example.org/r1.mp3"},
		{GUID: "r2", Title: "Episode 2", PubDate: 2, AudioURL: "https://example.org/r2.mp3"}})
	writeDemoArt(t, f2, color.RGBA{40, 120, 90, 255}, color.RGBA{250, 220, 80, 255})
	st.db.Exec(`UPDATE feeds SET image_checked=?`, time.Now().Unix()) // don't try to fetch real artwork
	st.SetSetting("first_feed", "")
}

// writeDemoArt: simple generated cover art.
func writeDemoArt(t *testing.T, feedID int64, bg, fg color.RGBA) {
	img := image.NewRGBA(image.Rect(0, 0, 300, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 300; x++ {
			c := bg
			dx, dy := x-150, y-150
			if dx*dx+dy*dy < 95*95 && dx*dx+dy*dy > 70*70 {
				c = fg
			}
			if (x > 135 && x < 165 && y > 60 && y < 240) || (y > 135 && y < 165 && x > 60 && x < 240) {
				c = fg
			}
			img.Set(x, y, c)
		}
	}
	name := fmt.Sprintf("podcast-%d-demo.jpg", feedID)
	fh, err := os.Create(filepath.Join(P.Images, name))
	if err != nil {
		t.Fatal(err)
	}
	jpeg.Encode(fh, img, &jpeg.Options{Quality: 90})
	fh.Close()
	openStoreForArt(feedID, name)
}

func openStoreForArt(feedID int64, name string) {
	st, err := openStore()
	if err != nil {
		return
	}
	defer st.Close()
	st.db.Exec(`UPDATE feeds SET image_file=?, image_url='demo' WHERE id=?`, name, feedID)
}
