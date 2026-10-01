package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

var progressRe = regexp.MustCompile(`progress\s*=\s*(\d+)%`)

func whisperThreads() int {
	n := runtime.NumCPU()
	if n > 8 {
		n = 8
	}
	return n
}

// runWhisper transcribes one wav and writes outBase+".json" (full format
// with tokens). See runWhisperFiles.
func runWhisper(ctx context.Context, modelPath, wav, outBase, lang, logPath string, extra []string, onProgress func(int)) (device string, err error) {
	dir := filepath.Dir(wav)
	return runWhisperFiles(ctx, modelPath, dir, [][2]string{{relPath(dir, wav), relPath(dir, outBase)}}, lang, logPath, extra,
		func(_, _, p int) {
			if onProgress != nil {
				onProgress(p)
			}
		})
}

// runWhisperFiles transcribes several wav files in ONE whisper-cli run (the
// model is loaded once). files: {input, output base} relative to dir; each
// result is written to <output base>.json. whisper-cli's own output goes to
// logPath. Returns the compute device whisper reported ("cuda", "vulkan", "metal",
// "cpu") - the truth, independent of settings. onProgress(fileIndex, files,
// percent of that file).
func runWhisperFiles(ctx context.Context, modelPath, dir string, files [][2]string, lang, logPath string, extra []string, onProgress func(int, int, int)) (device string, err error) {
	device = "cpu"
	if lang == "" {
		lang = "auto"
	}
	args := []string{
		"-m", relPath(dir, modelPath),
		"-l", lang,
		"-t", strconv.Itoa(whisperThreads()),
		"-ojf", // full JSON: segments + tokens with timestamps
		"-pp",  // print progress
	}
	for _, f := range files {
		args = append(args, "-f", f[0], "-of", f[1])
	}
	cmd := whisperCmd(ctx, dir, append(args, extra...)...)
	lf, err := os.Create(logPath)
	if err != nil {
		return device, err
	}
	defer lf.Close()
	shown := cmd.Args[1:]
	if len(shown) > 40 {
		shown = append(append([]string{}, shown[:40]...), fmt.Sprintf("... (%d files)", len(files)))
	}
	fmt.Fprintf(lf, "cmd: %s %s\n\n", cmd.Path, strings.Join(shown, " "))

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return device, err
	}
	cmd.Stdout = lf // transcript lines printed by whisper-cli; useful to look at
	if err := cmd.Start(); err != nil {
		return device, fmt.Errorf("start whisper-cli: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var tail []string // last lines of stderr for error messages
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		last, fileIdx := -1, -1
		for sc.Scan() {
			line := sc.Text()
			fmt.Fprintln(lf, line)
			ll := strings.ToLower(line)
			if strings.Contains(ll, "found gpu device") || strings.Contains(ll, "cuda devices") || strings.Contains(ll, "vulkan devices") {
				switch {
				case strings.Contains(ll, "cuda"):
					device = "cuda"
				case strings.Contains(ll, "vulkan"):
					device = "vulkan"
				}
			}
			// macOS (Homebrew build): "ggml_metal_init: found device: Apple M3" and similar
			if strings.Contains(ll, "ggml_metal") && (strings.Contains(ll, "found device") || strings.Contains(ll, "gpu name")) {
				device = "metal"
			}
			if strings.Contains(line, ": processing '") {
				fileIdx++
				last = -1
				if onProgress != nil {
					onProgress(fileIdx, len(files), 0)
				}
				continue
			}
			if m := progressRe.FindStringSubmatch(line); m != nil {
				if p, _ := strconv.Atoi(m[1]); p != last {
					last = p
					if onProgress != nil {
						onProgress(max(fileIdx, 0), len(files), p)
					}
				}
				continue
			}
			tail = append(tail, line)
			if len(tail) > 15 {
				tail = tail[1:]
			}
		}
		io.Copy(lf, stderr)
	}()
	wg.Wait()
	err = cmd.Wait()
	if ctx.Err() != nil {
		return device, ctx.Err()
	}
	if err != nil {
		return device, fmt.Errorf("whisper-cli failed: %v\n  last output:\n    %s", err, strings.Join(tail, "\n    "))
	}
	return device, nil
}

type whisperJSON struct {
	Transcription []struct {
		Offsets struct {
			From int64 `json:"from"`
			To   int64 `json:"to"`
		} `json:"offsets"`
		Text   string `json:"text"`
		Tokens []struct {
			Text    rawText `json:"text"`
			Offsets struct {
				From int64 `json:"from"`
				To   int64 `json:"to"`
			} `json:"offsets"`
			P float64 `json:"p"`
		} `json:"tokens"`
	} `json:"transcription"`
}

// parseWhisperJSON reads whisper-cli -ojf output. Special tokens like
// [_BEG_] or [_TT_123] are dropped. Invalid UTF-8 (a multibyte character split
// across tokens) is tolerated by Go's JSON decoder; for display we prefer
// segment text, which is always complete.
func parseWhisperJSON(path string) ([]Segment, []Token, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var wj whisperJSON
	if err := json.Unmarshal(b, &wj); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var segs []Segment
	var toks []Token
	for i, s := range wj.Transcription {
		text := strings.TrimSpace(s.Text)
		segs = append(segs, Segment{Idx: i, StartMs: s.Offsets.From, EndMs: s.Offsets.To, Text: text})
		ti := 0
		for _, t := range s.Tokens {
			text := string(t.Text)
			if strings.HasPrefix(text, "[_") || text == "" {
				continue
			}
			toks = append(toks, Token{
				SegIdx: i, Idx: ti,
				StartMs: t.Offsets.From, EndMs: t.Offsets.To,
				Text: text, P: t.P,
			})
			ti++
		}
	}
	return segs, toks, nil
}

// rawText decodes a JSON string but keeps invalid UTF-8 bytes as they are.
// whisper.cpp may split one multi-byte character (é, ü, ...) across two
// tokens; Go's normal decoder would turn each half into U+FFFD. Keeping the
// raw bytes lets the halves join back into the correct character when tokens
// are concatenated into words.
type rawText string

func (r *rawText) UnmarshalJSON(b []byte) error {
	if len(b) < 2 || b[0] != '"' || b[len(b)-1] != '"' {
		return fmt.Errorf("expected JSON string")
	}
	b = b[1 : len(b)-1]
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c != '\\' || i+1 >= len(b) {
			out = append(out, c)
			continue
		}
		i++
		switch b[i] {
		case 'n':
			out = append(out, '\n')
		case 't':
			out = append(out, '\t')
		case 'r':
			out = append(out, '\r')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'u':
			if i+4 < len(b) {
				if v, err := strconv.ParseUint(string(b[i+1:i+5]), 16, 32); err == nil {
					out = utf8.AppendRune(out, rune(v))
					i += 4
					continue
				}
			}
			out = append(out, '\\', 'u')
		default: // \" \\ \/
			out = append(out, b[i])
		}
	}
	*r = rawText(out)
	return nil
}
