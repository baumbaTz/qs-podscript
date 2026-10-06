package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Transcription in pieces.
//
// Whisper reads long audio in 30 s windows one after another and after each
// window decides itself where the next one starts. On crosstalk, laughter,
// music or movie clips it sometimes jumps too far and silently skips speech,
// or gets stuck repeating a phrase. Here QS-PodScript decides the pieces instead:
// the whole episode (not only detected speech - songs and music stay in) is
// cut into pieces of at most ~28 s, preferably in pauses or at speaker
// changes, and every piece is transcribed on its own. Nothing can be
// skipped: a piece without text but with detected speech gets an "[unclear]"
// marker. All pieces run in ONE whisper-cli call, so the model loads once.

const (
	chunkMaxMs = 28000 // below whisper's 30 s window
	chunkMinMs = 8000  // don't cut into tiny pieces
)

type span struct{ a, b int64 }

func whisperChunked(st *Store) bool { return st.Setting("whisper_chunks", "1") == "1" }

// speechUnion merges the diarization turns into non-overlapping speech spans.
func speechUnion(turns []Turn) []span {
	ts := append([]Turn(nil), turns...)
	sort.Slice(ts, func(i, j int) bool { return ts[i].StartMs < ts[j].StartMs })
	var out []span
	for _, t := range ts {
		if t.EndMs <= t.StartMs {
			continue
		}
		if n := len(out); n > 0 && t.StartMs <= out[n-1].b {
			out[n-1].b = max64(out[n-1].b, t.EndMs)
			continue
		}
		out = append(out, span{t.StartMs, t.EndMs})
	}
	return out
}

// planChunks cuts [0,totalMs] into consecutive pieces of at most chunkMaxMs.
// Cut points are chosen, in order of preference: middle of the longest pause
// between speech, a speaker change, otherwise a hard cut at the maximum.
func planChunks(turns []Turn, totalMs int64) []span {
	if totalMs <= 0 {
		return nil
	}
	type cand struct {
		at    int64
		score int64
	}
	var cands []cand
	speech := speechUnion(turns)
	prevEnd := int64(0)
	for _, s := range speech {
		if gap := s.a - prevEnd; gap > 0 {
			cands = append(cands, cand{prevEnd + gap/2, 1000 + gap}) // pause: best
		}
		prevEnd = s.b
	}
	if totalMs > prevEnd {
		cands = append(cands, cand{prevEnd + (totalMs-prevEnd)/2, 1000 + totalMs - prevEnd})
	}
	for _, t := range turns {
		cands = append(cands, cand{t.EndMs, 100}) // speaker change / end of turn
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].at < cands[j].at })

	var out []span
	pos := int64(0)
	for pos < totalMs {
		if totalMs-pos <= chunkMaxMs {
			out = append(out, span{pos, totalMs})
			break
		}
		// never leave a tiny last piece: cuts must leave >= chunkMinMs behind
		limit := min64(pos+chunkMaxMs, totalMs-chunkMinMs)
		best, bestScore := limit, int64(-1)
		for _, c := range cands {
			if c.at <= pos+chunkMinMs {
				continue
			}
			if c.at > limit {
				break
			}
			if c.score > bestScore || (c.score == bestScore && c.at > best) {
				best, bestScore = c.at, c.score
			}
		}
		out = append(out, span{pos, best})
		pos = best
	}
	return out
}

// writeWav16 writes 16 kHz mono 16-bit PCM.
func writeWav16(path string, s []float32) error {
	b := make([]byte, 44+2*len(s))
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*len(s)))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], sampleRate)
	binary.LittleEndian.PutUint32(b[28:], sampleRate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*len(s)))
	for i, x := range s {
		x = max(-1, min(1, x))
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(int16(x*32767)))
	}
	return os.WriteFile(path, b, 0o644)
}

type chunkResult struct {
	segs []Segment
	toks []Token
}

// transcribeInPieces runs whisper on all pieces and returns segments/tokens
// on the episode timeline. Pieces whose text loops get a second attempt with
// different decoding; loops that remain are cut down and marked.
func transcribeInPieces(ctx context.Context, st *Store, modelPath string, samples []float32, turns []Turn,
	lang, work string, extra []string) (device string, segs []Segment, toks []Token, err error) {

	totalMs := int64(len(samples)) * 1000 / sampleRate
	pieces := planChunks(turns, totalMs)
	dir := filepath.Join(work, "pieces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, nil, err
	}
	var files [][2]string
	for i, p := range pieces {
		a, b := int(p.a*sampleRate/1000), int(p.b*sampleRate/1000)
		b = min(b, len(samples))
		name := fmt.Sprintf("p%04d", i)
		if err := writeWav16(filepath.Join(dir, name+".wav"), samples[a:b]); err != nil {
			return "", nil, nil, err
		}
		files = append(files, [2]string{name + ".wav", name})
	}
	logf("   transcribing %d pieces of up to %ds...", len(pieces), chunkMaxMs/1000)

	run := func(idx []int, args []string, logName string) (string, map[int]chunkResult, error) {
		var fs [][2]string
		for _, i := range idx {
			fs = append(fs, files[i])
		}
		dev, err := runWhisperFiles(ctx, modelPath, dir, fs, lang, filepath.Join(work, logName), args, func(fi, n, pct int) {
			total := (fi*100 + pct) / max(n, 1)
			progressf("   transcribing: %d%% (piece %d of %d)", total, fi+1, n)
			setStage(fmt.Sprintf("Transcribing (piece %d of %d)", fi+1, n), total)
		})
		if err != nil {
			return dev, nil, err
		}
		res := map[int]chunkResult{}
		missing := 0
		for _, i := range idx {
			s, t, err := parseWhisperJSON(filepath.Join(dir, files[i][1]+".json"))
			if err != nil {
				// one bad piece must not fail the episode: it becomes [unclear]
				debugf("piece %d: %v", i, err)
				missing++
				continue
			}
			res[i] = chunkResult{s, t}
		}
		if missing == len(idx) {
			return dev, nil, fmt.Errorf("whisper wrote no results (see whisper.log)")
		}
		return dev, res, nil
	}

	all := make([]int, len(pieces))
	for i := range all {
		all[i] = i
	}
	device, res, err := run(all, extra, "whisper.log")
	if err != nil {
		return device, nil, nil, err
	}

	// pieces that repeat a phrase suspiciously often get up to two more
	// attempts with sampling instead of beam search. A retry is only kept if
	// it repeats LESS - when the hosts really said something four times,
	// the retry hears it four times too and the original stays.
	retries := [][]string{
		{"-tp", "0.4", "-nf", "-bs", "1", "-bo", "1"},
		{"-tp", "0.7", "-nf", "-bs", "1", "-bo", "1"},
	}
	fixed := 0
	for round, rargs := range retries {
		var suspicious []int
		for i := range pieces {
			if repetitionSuspicious(res[i].segs) {
				suspicious = append(suspicious, i)
			}
		}
		if len(suspicious) == 0 || ctx.Err() != nil {
			break
		}
		if round == 0 {
			logf("   %d piece(s) repeat a phrase suspiciously often - transcribing them again with different settings", len(suspicious))
		}
		_, res2, err := run(suspicious, append(append([]string{}, extra...), rargs...), fmt.Sprintf("whisper-retry%d.log", round+1))
		if err != nil {
			if ctx.Err() != nil {
				return device, nil, nil, ctx.Err()
			}
			logf("   warning: retry failed: %v", err)
			break
		}
		for _, i := range suspicious {
			if r2, ok := res2[i]; ok && hasLetters(r2.segs) && maxRepeats(r2.segs) < maxRepeats(res[i].segs) {
				res[i] = r2
				fixed++
			}
		}
	}
	if fixed > 0 {
		logf("   repetition reduced in %d piece(s)", fixed)
	}

	// assemble on the episode timeline
	trimmed, unclear := 0, 0
	for i, p := range pieces {
		r := res[i]
		s, t, n := trimLoops(r.segs, r.toks)
		trimmed += n
		speechMs := speechIn(turns, p.a, p.b)
		if !hasLetters(s) {
			if speechMs >= 1500 {
				// speech was detected but whisper gave nothing: mark, don't drop
				a, b := firstLastSpeech(turns, p.a, p.b)
				s = []Segment{{StartMs: a - p.a, EndMs: b - p.a, Text: "[unclear]"}}
				t = []Token{{StartMs: a - p.a, EndMs: b - p.a, Text: " [unclear]", P: 0}}
				unclear++
			} else {
				continue
			}
		}
		base := len(segs)
		for k := range s {
			s[k].Idx = base + k
			s[k].StartMs += p.a
			s[k].EndMs += p.a
			segs = append(segs, s[k])
		}
		for _, tk := range t {
			tk.SegIdx += base
			tk.StartMs += p.a
			tk.EndMs += p.a
			if speechMs == 0 && tk.P > 0.3 {
				tk.P = 0.3 // no voice detected here (music?) - show as uncertain
			}
			toks = append(toks, tk)
		}
	}
	if trimmed > 0 {
		logf("   %d repeating phrase(s) could not be fixed - cut down and marked with [...]", trimmed)
	}
	if unclear > 0 {
		logf("   %d piece(s) with speech but no text - marked [unclear]", unclear)
	}
	return device, segs, toks, nil
}

func speechIn(turns []Turn, a, b int64) int64 {
	var sum int64
	for _, s := range speechUnion(turns) {
		if lo, hi := max64(a, s.a), min64(b, s.b); hi > lo {
			sum += hi - lo
		}
	}
	return sum
}

func firstLastSpeech(turns []Turn, a, b int64) (int64, int64) {
	lo, hi := b, a
	for _, s := range speechUnion(turns) {
		if x, y := max64(a, s.a), min64(b, s.b); y > x {
			lo, hi = min64(lo, x), max64(hi, y)
		}
	}
	if hi <= lo {
		return a, b
	}
	return lo, hi
}

func hasLetters(segs []Segment) bool {
	for _, s := range segs {
		for _, r := range s.Text {
			if unicode.IsLetter(r) && s.Text != "[unclear]" {
				return true
			}
		}
	}
	return false
}

// loopMinReps: how often a phrase must repeat back to back to count as a
// whisper loop. People do repeat themselves ("no, no, no" / a chant 2-4
// times); 6+ identical 3-12 word phrases in one 28 s piece is the machine.
const loopMinReps = 6

// maxRepeats: how often the most repeated phrase (2-12 words) occurs back to
// back in these segments (1 = no repetition).
func maxRepeats(segs []Segment) int {
	var ws []string
	for _, s := range segs {
		for _, f := range strings.Fields(s.Text) {
			ws = append(ws, normWord(f))
		}
	}
	best := 1
	for i := range ws {
		for n := 2; n <= 12 && i+2*n <= len(ws); n++ {
			reps := 1
			for j := i + n; j+n <= len(ws); j += n {
				same := true
				for k := 0; k < n; k++ {
					if ws[j+k] != ws[i+k] {
						same = false
						break
					}
				}
				if !same {
					break
				}
				reps++
			}
			if reps > best && (n >= 3 || reps >= 6) {
				best = reps
			}
		}
	}
	return best
}

// repetitionSuspicious: worth a second attempt (not yet a loop to cut).
// 4+ times a phrase of 3+ words, or 6+ times a 2-word phrase.
func repetitionSuspicious(segs []Segment) bool { return maxRepeats(segs) >= 4 }

func chunkLoops(segs []Segment) bool {
	var ws []Word
	for _, s := range segs {
		for _, f := range strings.Fields(s.Text) {
			ws = append(ws, Word{Text: f})
		}
	}
	for _, l := range findLoops([]Utterance{{Words: ws}}) {
		if l.Times >= loopMinReps {
			return true
		}
	}
	return false
}

// trimLoops keeps the first 2 repetitions of a looping phrase, drops the
// rest and inserts a "[...]" marker (confidence 0) for the dropped time.
func trimLoops(segs []Segment, toks []Token) ([]Segment, []Token, int) {
	// words -> token index ranges
	type wref struct {
		norm     string
		from, to int // token indices [from,to]
	}
	var ws []wref
	for i, t := range toks {
		if i == 0 || strings.HasPrefix(t.Text, " ") || t.SegIdx != toks[i-1].SegIdx {
			ws = append(ws, wref{from: i, to: i})
		} else {
			ws[len(ws)-1].to = i
		}
	}
	for k := range ws {
		var sb strings.Builder
		for i := ws[k].from; i <= ws[k].to; i++ {
			sb.WriteString(toks[i].Text)
		}
		ws[k].norm = normWord(sb.String())
	}
	drop := make([]bool, len(toks))
	var markers []Token
	n := 0
	for i := 0; i < len(ws); {
		found := false
		for L := 3; L <= 12 && i+L*loopMinReps <= len(ws); L++ {
			reps := 1
			for j := i + L; j+L <= len(ws); j += L {
				same := true
				for k := 0; k < L; k++ {
					if ws[j+k].norm != ws[i+k].norm {
						same = false
						break
					}
				}
				if !same {
					break
				}
				reps++
			}
			if reps >= loopMinReps {
				first := ws[i+2*L].from
				last := ws[i+reps*L-1].to
				for x := first; x <= last; x++ {
					drop[x] = true
				}
				markers = append(markers, Token{SegIdx: toks[first].SegIdx, StartMs: toks[first].StartMs,
					EndMs: toks[last].EndMs, Text: " [...]", P: 0})
				n++
				i += reps * L
				found = true
				break
			}
		}
		if !found {
			i++
		}
	}
	if n == 0 {
		return segs, toks, 0
	}
	var out []Token
	mi := 0
	for i, t := range toks {
		if drop[i] {
			if mi < len(markers) && markers[mi].StartMs == t.StartMs {
				out = append(out, markers[mi])
				mi++
			}
			continue
		}
		out = append(out, t)
	}
	// rebuild segment texts from their remaining tokens
	text := map[int]*strings.Builder{}
	for _, t := range out {
		if text[t.SegIdx] == nil {
			text[t.SegIdx] = &strings.Builder{}
		}
		text[t.SegIdx].WriteString(t.Text)
	}
	var segsOut []Segment
	for _, s := range segs {
		if b := text[s.Idx]; b != nil {
			s.Text = strings.TrimSpace(b.String())
			segsOut = append(segsOut, s)
		}
	}
	// renumber segments and token references
	re := map[int]int{}
	for k := range segsOut {
		re[segsOut[k].Idx] = k
		segsOut[k].Idx = k
	}
	// the [...] markers have no index of their own and dropped tokens leave
	// gaps: number the tokens of every segment again, in order
	// ((version, seg_idx, idx) must be unique when stored)
	next := map[int]int{}
	for k := range out {
		out[k].SegIdx = re[out[k].SegIdx]
		out[k].Idx = next[out[k].SegIdx]
		next[out[k].SegIdx]++
	}
	return segsOut, out, n
}

func normWord(s string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(s), ".,!?;:\"'"))
}
