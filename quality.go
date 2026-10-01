package main

import (
	_ "embed"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// Transcript sanity check. whisper.cpp can produce garbage on some GPU setups
// (e.g. only "!!!!" - a numerical problem, usually with flash attention).
// That must never be stored silently.

// speechTestWav: 11 s of JFK's inaugural address (public domain), the standard
// whisper.cpp sample. Used by the self-check to verify real text output.
//
//go:embed speechtest.wav
var speechTestWav []byte

const speechTestExpect = "country" // "... ask what you can do for your country"

// transcriptProblem returns "" if the transcript looks plausible, else a
// short reason.
func transcriptProblem(segs []Segment, audioSec float64) string {
	var letters, nonSpace, words int
	counts := map[string]int{}
	top := 0
	for _, s := range segs {
		for _, w := range strings.Fields(s.Text) {
			hasLetter := false
			for _, r := range w {
				nonSpace++
				if unicode.IsLetter(r) {
					letters++
					hasLetter = true
				}
			}
			if hasLetter {
				words++
			}
		}
		t := strings.ToLower(strings.TrimSpace(s.Text))
		if t != "" {
			counts[t]++
			top = max(top, counts[t])
		}
	}
	minutes := audioSec / 60
	switch {
	case nonSpace == 0 && minutes > 2:
		return "no text at all"
	case nonSpace > 50 && float64(letters) < 0.5*float64(nonSpace):
		return fmt.Sprintf("mostly punctuation instead of words (%d%% letters)", letters*100/max(nonSpace, 1))
	case minutes > 10 && float64(words)/minutes < 30:
		return fmt.Sprintf("only %.0f words per minute", float64(words)/minutes)
	case len(segs) >= 30 && float64(top) > 0.4*float64(len(segs)):
		return "the same sentence repeated over and over"
	}
	return ""
}

// flashAttn: whether whisper may use flash attention (default yes; switched
// off automatically when it produced garbage on this computer).
func flashAttn(st *Store) bool { return st.Setting("whisper_flash_attn", "1") != "0" }

// Decoding settings against Whisper's two long-audio failure modes:
// repetition loops ("I don't care about that." x 30) and skipped passages.
// Both come mostly from feeding the previous 30 s window's text in as context.
const (
	vadModelFile = "ggml-silero-v6.2.0.bin"
	vadModelURL  = "https://huggingface.co/ggml-org/whisper-vad/resolve/main/" + vadModelFile
)

func vadModelPath() string { return filepath.Join(P.Models, vadModelFile) }

// whisperContext: carry text between 30 s windows (whisper default). Off by
// default in QS-PodScript - it is the main cause of loops and skipping.
func whisperContext(st *Store) bool { return st.Setting("whisper_context", "0") == "1" }

// vadArgs: voice activity detection for one podcast (Podcast settings).
// Skips music and silence - NOT for shows where people sing.
func vadArgs(f Feed) []string {
	if f.VAD && fileExists(vadModelPath()) {
		return []string{"--vad", "-vm", vadModelPath()}
	}
	return nil
}

// whisperExtraArgs: decoding options for episodes AND the self-test.
func whisperExtraArgs(st *Store) []string {
	var a []string
	if !flashAttn(st) {
		a = append(a, "-nfa")
	}
	if !whisperContext(st) {
		a = append(a, "-mc", "0")
	}
	// stricter repetition detection: repetitive output counts as a failed
	// decode and is retried with temperature fallback (default 2.4)
	a = append(a, "-et", "2.8")
	return a
}

// withoutFlag returns args without "-nfa" (used by the flash attention test).
func withFlashAttn(args []string, on bool) []string {
	var out []string
	for _, x := range args {
		if x != "-nfa" {
			out = append(out, x)
		}
	}
	if !on {
		out = append(out, "-nfa")
	}
	return out
}

type loopSpot struct {
	StartMs int64
	Phrase  string
	Times   int
}

// findLoops finds phrases (3-12 words) that repeat back to back at least 5
// times - Whisper's repetition loop - and returns where they start.
func findLoops(us []Utterance) []loopSpot {
	type w struct {
		t  string
		ms int64
	}
	var ws []w
	for _, u := range us {
		for _, x := range u.Words {
			ws = append(ws, w{strings.ToLower(strings.Trim(x.Text, ".,!?;:\"'")), x.StartMs})
		}
	}
	var out []loopSpot
	for i := 0; i < len(ws); {
		found := false
		for n := 3; n <= 12 && i+n*5 <= len(ws); n++ {
			reps := 1
			for j := i + n; j+n <= len(ws); j += n {
				same := true
				for k := 0; k < n; k++ {
					if ws[j+k].t != ws[i+k].t {
						same = false
						break
					}
				}
				if !same {
					break
				}
				reps++
			}
			if reps >= 5 {
				var p []string
				for k := 0; k < n; k++ {
					p = append(p, ws[i+k].t)
				}
				out = append(out, loopSpot{StartMs: ws[i].ms, Phrase: strings.Join(p, " "), Times: reps})
				i += n * reps
				found = true
				break
			}
		}
		if !found {
			i++
		}
	}
	return out
}

// findBrokenTranscripts checks the main version of every finished episode
// (optionally of one feed) and puts broken ones back at the front of the queue.
func findBrokenTranscripts(st *Store, feedID int64) (checked, broken int, err error) {
	eps, err := st.Episodes(feedID, "done", 0)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range eps {
		if !e.ActiveVersionID.Valid {
			continue
		}
		v, err := st.Version(e.ActiveVersionID.Int64)
		if err != nil {
			continue
		}
		segs, _, _, err := st.LoadResults(v.ID)
		if err != nil {
			continue
		}
		checked++
		if p := transcriptProblem(segs, v.AudioSeconds); p != "" {
			broken++
			logf("Episode %d (%s): transcript broken (%s) - queued again", e.ID, e.Title, p)
			st.QueueEpisode(e.ID)
		}
	}
	return checked, broken, nil
}
