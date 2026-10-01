package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverlapRegions(t *testing.T) {
	turns := []Turn{{0, 5000, 0}, {4000, 9000, 1}, {8500, 12000, 0}, {12000, 13000, 1}}
	got := overlapRegions(turns)
	want := []interval{{4000, 5000}, {8500, 9000}}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func tok(seg, idx int, a, b int64, text string) Token {
	return Token{SegIdx: seg, Idx: idx, StartMs: a, EndMs: b, Text: text}
}

func TestBuildUtterancesSplitAndCrosstalk(t *testing.T) {
	// A talks 0-4s, B talks 3.5-8s (overlap 3.5-4.0), silence after 8s
	turns := []Turn{{0, 4000, 0}, {3500, 8000, 1}}
	segs := []Segment{{0, 0, 8000, "one two three four five six seven"}, {1, 9000, 10000, "music"}}
	toks := []Token{
		tok(0, 0, 0, 1000, " one"), tok(0, 1, 1000, 2000, " two"), tok(0, 2, 2000, 3000, " three"),
		tok(0, 3, 3500, 4000, " four"), // fully inside overlap -> crosstalk, but a 1-token run gets absorbed
		tok(0, 4, 4500, 5500, " five"), tok(0, 5, 5500, 6500, " six"), tok(0, 6, 6500, 7500, " seven"),
		tok(1, 0, 9000, 10000, " music"),
	}
	us := buildUtterances(segs, toks, turns, nil)
	var lines []string
	for _, u := range us {
		lines = append(lines, labelName(u.Label)+":"+u.Text)
	}
	got := strings.Join(lines, " | ")
	want := "Speaker 1:one two three four | Speaker 2:five six seven | Unknown:music"
	if got != want {
		t.Fatalf("\n got: %s\nwant: %s", got, want)
	}
}

func TestLongCrosstalkRun(t *testing.T) {
	turns := []Turn{{0, 6000, 0}, {2000, 6000, 1}}
	segs := []Segment{{0, 0, 6000, "x"}}
	var toks []Token
	for i := 0; i < 6; i++ {
		toks = append(toks, tok(0, i, int64(i)*1000, int64(i+1)*1000, " w"))
	}
	us := buildUtterances(segs, toks, turns, nil)
	if len(us) != 2 || us[0].Label != 0 || us[1].Label != labelCrosstalk {
		t.Fatalf("unexpected: %+v", us)
	}
}

func TestParseFeed(t *testing.T) {
	xml := `<?xml version="1.0" encoding="ISO-8859-1"?>
<rss xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"><channel><title>Caf` + "\xe9" + `</title>
<item><title>B</title><guid>b</guid><pubDate>Tue, 5 Mar 2024 10:00:00 +0000</pubDate><enclosure url="http://x/b.mp3"/><itunes:duration>1:02:03</itunes:duration></item>
<item><title>A</title><pubDate>Mon, 04 Mar 2019 10:00:00 GMT</pubDate><enclosure url="http://x/a.mp3"/><itunes:duration>90</itunes:duration></item>
<item><title>no audio</title></item>
</channel></rss>`
	title, items, err := parseFeed([]byte(xml))
	if err != nil {
		t.Fatal(err)
	}
	if title != "Café" {
		t.Fatalf("title %q", title)
	}
	if len(items) != 2 || items[0].Title != "A" || items[0].GUID != "http://x/a.mp3" || items[0].DurationS != 90 || items[1].DurationS != 3723 {
		t.Fatalf("items %+v", items)
	}
}

func linesOf(us []Utterance) string {
	var l []string
	for _, u := range us {
		l = append(l, labelName(u.Label)+":"+u.Text)
	}
	return strings.Join(l, " | ")
}

func TestCorrections(t *testing.T) {
	// automatic result: speaker 0 for 0-4s, speaker 2 for 4-8s, speaker 1 for 8-12s
	turns := []Turn{{0, 4000, 0}, {4000, 8000, 2}, {8000, 12000, 1}}
	segs := []Segment{{0, 0, 12000, "x"}}
	var toks []Token
	words := []string{" a", " b", " c", " d", " e", " f", " g", " h", " i", " j", " k", " l"}
	for i, w := range words {
		toks = append(toks, tok(0, i, int64(i)*1000, int64(i+1)*1000, w))
	}
	base := linesOf(buildUtterances(segs, toks, turns, nil))
	if base != "Speaker 1:a b c d | Speaker 3:e f g h | Speaker 2:i j k l" {
		t.Fatalf("base: %s", base)
	}
	// merge: speaker 3 (cluster 2) is really speaker 1 (cluster 0)
	got := linesOf(buildUtterances(segs, toks, turns, []Correction{{Kind: "merge", From: 2, Label: 0}}))
	if got != "Speaker 1:a b c d e f g h | Speaker 2:i j k l" {
		t.Fatalf("merge: %s", got)
	}
	// a single word forced to another speaker must survive (not absorbed)
	got = linesOf(buildUtterances(segs, toks, turns, []Correction{{Kind: "range", StartMs: 9000, EndMs: 10000, Label: 0}}))
	if got != "Speaker 1:a b c d | Speaker 3:e f g h | Speaker 2:i | Speaker 1:j | Speaker 2:k l" {
		t.Fatalf("single word: %s", got)
	}
	// later range wins over earlier one; chained merges resolve
	got = linesOf(buildUtterances(segs, toks, turns, []Correction{
		{Kind: "range", StartMs: 0, EndMs: 12000, Label: 5},
		{Kind: "range", StartMs: 0, EndMs: 2000, Label: labelCrosstalk},
	}))
	if got != "[crosstalk]:a b | Speaker 6:c d e f g h i j k l" {
		t.Fatalf("override order: %s", got)
	}
	// a speaker created by a range correction can itself be merged
	got = linesOf(buildUtterances(segs, toks, turns, []Correction{
		{Kind: "range", StartMs: 0, EndMs: 2000, Label: 7},
		{Kind: "merge", From: 7, Label: 1},
	}))
	if got != "Speaker 2:a b | Speaker 1:c d | Speaker 3:e f g h | Speaker 2:i j k l" {
		t.Fatalf("merge of new speaker: %s", got)
	}
	m := mergeMap([]Correction{{Kind: "merge", From: 3, Label: 2}, {Kind: "merge", From: 2, Label: 0}, {Kind: "merge", From: 0, Label: 3}})
	if len(m) != 3 { // cycle must not hang
		t.Fatalf("cycle map %v", m)
	}
}

func TestTokensToWords(t *testing.T) {
	ts := []Token{tok(0, 0, 0, 100, " Hello"), tok(0, 1, 100, 150, ","), tok(0, 2, 200, 300, " wor"), tok(0, 3, 300, 400, "ld"), tok(0, 4, 400, 450, ".")}
	ws := tokensToWords(ts)
	if len(ws) != 2 || ws[0].Text != "Hello," || ws[1].Text != "world." || ws[1].StartMs != 200 || ws[1].EndMs != 450 {
		t.Fatalf("%+v", ws)
	}
}

func TestRawTextKeepsSplitUTF8(t *testing.T) {
	// "é" = C3 A9 split across two tokens, as whisper.cpp can emit it
	js := []byte("{\"transcription\":[{\"offsets\":{\"from\":0,\"to\":1000},\"text\":\" caf\xc3\xa9\",\"tokens\":[" +
		"{\"text\":\" caf\",\"offsets\":{\"from\":0,\"to\":500},\"p\":1}," +
		"{\"text\":\"\xc3\",\"offsets\":{\"from\":500,\"to\":700},\"p\":1}," +
		"{\"text\":\"\xa9 \\\"ok\\\"\\n\",\"offsets\":{\"from\":700,\"to\":1000},\"p\":1}]}]}")
	f := t.TempDir() + "/w.json"
	if err := os.WriteFile(f, js, 0o644); err != nil {
		t.Fatal(err)
	}
	_, toks, err := parseWhisperJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	ws := tokensToWords(toks)
	if len(ws) != 1 || ws[0].Text != "café \"ok\"" {
		t.Fatalf("%+v", ws)
	}
}

func TestPlanVoiceMerges(t *testing.T) {
	v := func(xs ...float32) []float32 { return xs }
	embs := []ClusterEmbedding{
		{Cluster: 0, Seconds: 30, Vec: v(1, 0, 0)},
		{Cluster: 1, Seconds: 30, Vec: v(0, 1, 0)},
		{Cluster: 2, Seconds: 5, Vec: v(0.95, 0.1, 0)},   // fragment of 0
		{Cluster: 3, Seconds: 3, Vec: v(0.1, 0.97, 0.1)}, // fragment of 1
		{Cluster: 4, Seconds: 10, Vec: v(0, 0, 1)},       // someone else
	}
	talk := map[int]int64{0: 600000, 1: 500000, 2: 20000, 3: 9000, 4: 40000}
	ms := planVoiceMerges(embs, talk, 0.6)
	got := map[int]int{}
	for _, m := range ms {
		got[m.From] = m.Into
	}
	if len(ms) != 2 || got[2] != 0 || got[3] != 1 {
		t.Fatalf("%+v", ms)
	}
	if n := len(planVoiceMerges(embs, talk, 0)); n != 4 { // everything merges at 0... down to one voice
		t.Fatalf("min 0 merged %d", n)
	}
}

func TestWhisperDeviceSummary(t *testing.T) {
	cpu := "whisper_backend_init_gpu: device 0: CPU (type: 0)\nwhisper_init_with_params_no_state: use gpu    = 1"
	if got := whisperDeviceSummary(cpu); got != "CPU" {
		t.Fatalf("cpu build: %q", got)
	}
	cuda := "ggml_cuda_init: found 1 CUDA devices (Total VRAM: 7885 MiB):\n  Device 0: NVIDIA GeForce RTX 3070, compute capability 8.6, VMM: yes\nwhisper_backend_init_gpu: device 0: CUDA0 (type: 1)"
	if got := whisperDeviceSummary(cuda); !strings.HasPrefix(got, "CUDA - ") {
		t.Fatalf("cuda build: %q", got)
	}
	vk := "ggml_vulkan: Found 1 Vulkan devices:\nggml_vulkan: 0 = AMD Radeon RX 6700 XT (RADV NAVI22)"
	if got := whisperDeviceSummary(vk); !strings.HasPrefix(got, "Vulkan - ") {
		t.Fatalf("vulkan build: %q", got)
	}
}

func TestBuildInfoMustMatchBinary(t *testing.T) {
	P.Tools = t.TempDir()
	os.MkdirAll(whisperDir(), 0o755)
	bin := filepath.Join(whisperDir(), exeName("whisper-cli"))
	os.WriteFile(bin, []byte("cuda build"), 0o755)
	sum, _ := fileSHA256(bin)
	os.WriteFile(filepath.Join(whisperDir(), "BUILD_INFO"), []byte("backend=cuda\nsha256="+sum+"\n"), 0o644)
	if got := customWhisperBackend(); got != "cuda" {
		t.Fatalf("matching binary: %q", got)
	}
	os.WriteFile(bin, []byte("some other cpu build"), 0o755) // replaced
	if got := customWhisperBackend(); got != "" {
		t.Fatalf("replaced binary must not count as cuda build: %q", got)
	}
	os.WriteFile(filepath.Join(whisperDir(), "BUILD_INFO"), []byte("backend=vulkan\n"), 0o644) // old marker without sha
	if got := customWhisperBackend(); got != "vulkan" {
		t.Fatalf("old marker: %q", got)
	}
}

func TestVulkanCapable(t *testing.T) {
	cases := map[string]bool{
		"AMD Radeon RX 9070 XT":           true,
		"AMD Radeon RX 7900 XTX":          true,
		"NVIDIA GeForce RTX 3070":         true,
		"AMD Radeon(TM) Graphics":         false, // APU integrated
		"Intel(R) UHD Graphics 770":       false,
		"Intel(R) Arc(TM) A770 Graphics":  true,
		"Microsoft Basic Display Adapter": false,
		"Parsec Virtual Display Adapter":  false,
	}
	for name, want := range cases {
		if got := vulkanCapable(name); got != want {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
}

func TestTranscriptProblem(t *testing.T) {
	mk := func(n int, text string, dur int64) []Segment {
		var s []Segment
		for i := 0; i < n; i++ {
			s = append(s, Segment{Idx: i, StartMs: int64(i) * dur, EndMs: int64(i+1) * dur, Text: text})
		}
		return s
	}
	// the real failure: 185 segments of "!!!!" for 1.5 h
	if p := transcriptProblem(mk(185, " ! ! ! ! ! ! ! ! ! ! !", 29000), 5400); p == "" {
		t.Fatal("exclamation marks not detected")
	}
	// normal speech, ~160 words/min
	var ok []Segment
	for i := 0; i < 900; i++ {
		ok = append(ok, Segment{Idx: i, Text: fmt.Sprintf("So I think the movie number %d was really quite good, honestly.", i)})
	}
	if p := transcriptProblem(ok, 5400); p != "" {
		t.Fatalf("normal transcript flagged: %s", p)
	}
	// hallucination loop
	if p := transcriptProblem(mk(200, "Thank you for watching.", 5000), 1000); p == "" {
		t.Fatal("repetition not detected")
	}
	// far too few words for a long episode
	if p := transcriptProblem(mk(20, "Hello there.", 3000), 3600); p == "" {
		t.Fatal("too few words not detected")
	}
	// short clip without speech is fine
	if p := transcriptProblem(nil, 60); p != "" {
		t.Fatalf("short silence flagged: %s", p)
	}
}

func TestFindLoops(t *testing.T) {
	mk := func(text string, start int64) Utterance {
		var ws []Word
		for i, x := range strings.Fields(text) {
			ws = append(ws, Word{Text: x, StartMs: start + int64(i)*300})
		}
		return Utterance{Words: ws}
	}
	loop := "ie Lannister. I don't care that you did your brother anyway." + strings.Repeat(" I don't care about that.", 14)
	us := []Utterance{mk("So this week we watched a movie.", 0), mk(loop, 437000), mk("Anyway, back to the movie.", 500000)}
	ls := findLoops(us)
	if len(ls) != 1 || ls[0].Phrase != "i don't care about that" || ls[0].Times < 10 {
		t.Fatalf("%+v", ls)
	}
	if n := len(findLoops([]Utterance{mk("no no no no no no that is fine yes yes", 0)})); n != 0 {
		t.Fatalf("short words flagged: %d", n) // single-word repeats are normal speech
	}
}
