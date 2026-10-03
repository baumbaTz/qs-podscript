package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const version = "0.31.1"

const usageText = `QS-PodScript ` + version + ` - podcast transcription with speaker detection

Usage: qs-podscript              start the web interface (same as "serve")
       qs-podscript <command> [options]

Web interface:
  serve [--port 8321] [--no-browser]      run the local web interface

Homeserver (shared, multi-user):
  server [--listen 127.0.0.1:8322]        run as server: public read-only pages, logins,
                                          editors fix the podcasts assigned to them
  user list | add <name> [--admin] | passwd <name> | admin <name> | delete <name>

Setup:
  setup [--model NAME] [--cpu] [--force]   download ffmpeg, whisper.cpp and models
                                          models: ` + "turbo (default), turbo-q5, large-v3, medium.en, small.en, base.en" + `
  check                                   self-test: ffmpeg, whisper (GPU?), diarization
  config [key] [value]                    show or change settings:
                                            diarize_model (resnet34 | resnet152 | resnet221 | resnet293 |
                                              titanet | titanet-large | eres2net | campplus | campplus-3d)
                                            diarize_step (0.1 | 0.25 | 0.5 = 1 / 2.5 / 5 s steps)
                                            diarize_threshold (of the current model; lower = more voices)
                                            speaker_device (auto | cpu: graphics card when it works)
                                            voice_merge (0-0.99, default 0 = off, try 0.92; merges
                                              voices with similar voiceprints after detection)
                                            keep_audio (1/0, default 1)

Feeds:
  feed add <url> [--lang en] [--speakers N] [--names "A,B,C"]
  feed set <feed-id> [--lang en] [--speakers N] [--names "A,B,C"]
  feed list
  feed refresh [feed-id]

Episodes:
  episodes [--feed ID] [--status new|done|error|...] [--limit N]
  run [--feed ID] [--limit N] [--retry-errors] [--no-refresh] [--keep-work]
                                          process the queue, oldest episode first
  process <episode-id> [--keep-work]      (re)process one episode now -> new version
  rediarize <episode-id> [--version ID]   redo only speaker detection (current settings),
                                          reusing the transcript -> new version
  verify [--feed ID]                      find broken transcripts (e.g. only "!!!") and queue
                                          them again
  versions <episode-id>                   list all versions of an episode
  export <file.zip>                      database + audio into one file (moving to another computer)
  import <file.zip>                      replace all data here with an export (current data kept as backup)
  show <episode-id> [--version ID] [--out file.txt]
                                          print the speaker-labeled transcript

Everything is stored in the "data" folder next to this program.
Log file: data/qs-podscript.log  (send this when something goes wrong)
Stop a running job with Ctrl+C - the current episode is put back in the queue.
`

func main() {
	prepareCUDAPath() // may restart the program once (Linux, graphics card support installed)
	if err := initPaths(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	initLogging()
	internal := len(os.Args) > 1 && (os.Args[1] == "gpu-check" || os.Args[1] == "diarize-file")
	if !internal { // never from a helper process while the app itself runs
		if err := applyStagedImport(); err != nil { // data from another computer, prepared before the restart
			logf("ERROR: %v", err)
		}
	}

	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"serve"} // double-click: start the web interface
	}

	ctx := setupInterrupts(args[0] == "run")

	var err error
	switch args[0] {
	case "serve":
		err = cmdServe(ctx, args[1:])
	case "server":
		err = cmdServer(ctx, args[1:])
	case "user", "users":
		err = cmdUser(args[1:])
	case "setup":
		err = cmdSetup(ctx, args[1:])
	case "check":
		err = cmdCheck(ctx, args[1:])
	case "feed", "feeds":
		err = cmdFeed(ctx, args[1:])
	case "episodes", "eps":
		err = cmdEpisodes(args[1:])
	case "run":
		err = cmdRun(ctx, args[1:])
	case "process":
		err = cmdProcess(ctx, args[1:])
	case "config":
		err = cmdConfig(args[1:])
	case "gpu-check":
		err = cmdGPUCheck()
	case "diarize-file":
		err = cmdDiarizeFile(args[1:])
	case "speaker-bench":
		err = cmdSpeakerBench(args[1:])
	case "rediarize":
		err = cmdRediarize(ctx, args[1:])
	case "verify":
		err = cmdVerify(args[1:])
	case "versions":
		err = cmdVersions(args[1:])
	case "show":
		err = cmdShow(args[1:])
	case "export":
		err = cmdExport(args[1:])
	case "import":
		err = cmdImport(args[1:])
	case "help", "-h", "--help":
		fmt.Print(usageText)
	case "version", "--version":
		fmt.Println("QS-PodScript", version)
	default:
		err = fmt.Errorf("unknown command %q (run without arguments for help)", args[0])
	}
	if err != nil {
		logf("ERROR: %v", err)
		pauseIfOwnConsole()
		os.Exit(1)
	}
}

// softStop is cancelled by the first Ctrl+C during "run": the current episode
// is finished, then the queue stops. The second Ctrl+C cancels the returned
// hard context and aborts immediately (episode goes back into the queue).
var softStop context.Context = context.Background()

func setupInterrupts(graceful bool) context.Context {
	hard, hardCancel := context.WithCancel(context.Background())
	soft, softCancel := context.WithCancel(context.Background())
	softStop = soft
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt)
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM) // systemd stop / kill: abort right away, episode is requeued
	go func() {
		<-term
		softCancel()
		hardCancel()
	}()
	go func() {
		<-sig
		softCancel()
		if graceful {
			logf(">> Stopping after the current episode. Press Ctrl+C again to abort right now.")
			<-sig
			logf(">> Aborting...")
		}
		hardCancel()
	}()
	return hard
}

// ------------------------------------------------------------ feed

func feedFlags(name string) (*flag.FlagSet, *string, *int, *string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	lang := fs.String("lang", "", "spoken language code (en, de, ...) or auto")
	speakers := fs.Int("speakers", -1, "number of regular speakers (0 = detect automatically)")
	names := fs.String("names", "", "comma-separated speaker names")
	return fs, lang, speakers, names
}

// parseInterleaved lets flags come before or after positional arguments
// ("feed add URL --lang en" and "feed add --lang en URL" both work).
func parseInterleaved(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		fs.Parse(args)
		args = fs.Args()
		if len(args) == 0 {
			return pos
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func cmdFeed(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: qs-podscript feed add|set|list|refresh")
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	switch args[0] {
	case "add":
		fs, lang, speakers, names := feedFlags("feed add")
		pos := parseInterleaved(fs, args[1:])
		if len(pos) != 1 {
			return fmt.Errorf("usage: qs-podscript feed add <url> [--lang en] [--speakers N] [--names \"A,B\"]")
		}
		f := Feed{URL: pos[0], Language: "auto"}
		if *lang != "" {
			f.Language = *lang
		}
		if *speakers > 0 {
			f.NumSpeakers = *speakers
		}
		f.SpeakerNames = cleanNames(*names)
		logf("Fetching feed %s ...", f.URL)
		title, items, err := fetchFeed(ctx, f.URL)
		if err != nil {
			return err
		}
		f.Title = title
		id, err := st.AddFeed(f)
		if err != nil {
			if errors.Is(err, errDuplicateFeed) || strings.Contains(err.Error(), "UNIQUE") {
				return fmt.Errorf("this feed was already added")
			}
			return err
		}
		srcs, _ := st.Sources(id)
		if len(srcs) == 0 {
			return fmt.Errorf("feed source missing")
		}
		n, err := st.UpsertEpisodes(id, srcs[0].ID, items)
		if err != nil {
			return err
		}
		st.UpdateFeedMeta(id, title)
		logf("Added feed %d: %s (%d episodes)", id, title, n)
		if len(items) > 0 {
			logf("Oldest: %s  |  Newest: %s",
				time.Unix(items[0].PubDate, 0).Format("2006-01-02"),
				time.Unix(items[len(items)-1].PubDate, 0).Format("2006-01-02"))
		}
		if f.Language == "auto" {
			logf("Tip: set the language (e.g. 'qs-podscript feed set %d --lang en') - faster and avoids misdetection.", id)
		}
		return nil

	case "set":
		fs, lang, speakers, names := feedFlags("feed set")
		pos := parseInterleaved(fs, args[1:])
		if len(pos) != 1 {
			return fmt.Errorf("usage: qs-podscript feed set <feed-id> [--lang en] [--speakers N] [--names \"A,B\"]")
		}
		id, err := strconv.ParseInt(pos[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid feed id %q", pos[0])
		}
		f, err := st.Feed(id)
		if err != nil {
			return err
		}
		if *lang != "" {
			f.Language = *lang
		}
		if *speakers >= 0 {
			f.NumSpeakers = *speakers
		}
		if *names != "" {
			f.SpeakerNames = cleanNames(*names)
		}
		if err := st.UpdateFeedSettings(f); err != nil {
			return err
		}
		logf("Feed %d: language=%s speakers=%s names=%s", f.ID, f.Language, speakersStr(f.NumSpeakers), orDash(f.SpeakerNames))
		return nil

	case "list":
		feeds, err := st.Feeds()
		if err != nil {
			return err
		}
		if len(feeds) == 0 {
			fmt.Println("No feeds yet. Add one with: qs-podscript feed add <url>")
			return nil
		}
		for _, f := range feeds {
			c, _ := st.StatusCounts(f.ID)
			total := 0
			for _, n := range c {
				total += n
			}
			fmt.Printf("[%d] %s\n     %s\n     lang=%s speakers=%s names=%s\n     episodes: %d total, %d done, %d new, %d error\n",
				f.ID, f.Title, f.URL, f.Language, speakersStr(f.NumSpeakers), orDash(f.SpeakerNames),
				total, c["done"], c["new"]+c["queued"], c["error"])
		}
		return nil

	case "refresh":
		var id int64
		if len(args) > 1 {
			id, _ = strconv.ParseInt(args[1], 10, 64)
		}
		return refreshFeeds(ctx, st, id)
	}
	return fmt.Errorf("unknown feed command %q", args[0])
}

func refreshFeeds(ctx context.Context, st *Store, onlyID int64) error {
	feeds, err := st.Feeds()
	if err != nil {
		return err
	}
	for _, f := range feeds {
		if onlyID > 0 && f.ID != onlyID {
			continue
		}
		refreshPodcast(ctx, st, f) // logs per feed; a failing feed doesn't stop the others
	}
	return nil
}

func cleanNames(s string) string {
	var out []string
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return strings.Join(out, ",")
}

func speakersStr(n int) string {
	if n <= 0 {
		return "auto"
	}
	return strconv.Itoa(n)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ------------------------------------------------------------ episodes

func cmdEpisodes(args []string) error {
	fs := flag.NewFlagSet("episodes", flag.ExitOnError)
	feedID := fs.Int64("feed", 0, "only this feed")
	status := fs.String("status", "", "only episodes with this status")
	limit := fs.Int("limit", 0, "max number of rows")
	fs.Parse(args)
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	eps, err := st.Episodes(*feedID, *status, *limit)
	if err != nil {
		return err
	}
	for _, e := range eps {
		ver := "-"
		if e.ActiveVersionID.Valid {
			ver = fmt.Sprintf("v%d", e.ActiveVersionID.Int64)
		}
		fmt.Printf("%6d  %s  %-10s %-5s %s\n", e.ID, time.Unix(e.PubDate, 0).Format("2006-01-02"), e.Status, ver, truncate(e.Title, 70))
		if e.Status == "error" && e.Error != "" {
			fmt.Printf("        error: %s\n", truncate(strings.ReplaceAll(e.Error, "\n", " "), 100))
		}
	}
	fmt.Printf("(%d episodes)\n", len(eps))
	return nil
}

func cmdExport(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: qs-podscript export <file.zip>")
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	f, err := os.Create(args[0])
	if err != nil {
		return err
	}
	if err := exportTo(f, st); err != nil {
		f.Close()
		os.Remove(args[0])
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	logf("Exported to %s", args[0])
	return nil
}

func cmdImport(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: qs-podscript import <file.zip>   (replaces all data here; the current data is kept in data/before-import-...)")
	}
	if c, err := net.DialTimeout("tcp", "127.0.0.1:8321", 300*time.Millisecond); err == nil {
		c.Close()
		return fmt.Errorf("QS-PodScript is running - close it first (or use Import on its Setup page)")
	}
	info, err := stageImport(args[0])
	if err != nil {
		return err
	}
	if err := applyStagedImport(); err != nil {
		return err
	}
	logf("Imported %d podcasts, %d transcribed episodes (from QS-PodScript %s).", info.Podcasts, info.Episodes, info.Version)
	return nil
}

func cmdVersions(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: qs-podscript versions <episode-id>")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid episode id %q", args[0])
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	ep, err := st.Episode(id)
	if err != nil {
		return err
	}
	vs, err := st.Versions(id)
	if err != nil {
		return err
	}
	fmt.Printf("Episode %d: %s\n", ep.ID, ep.Title)
	for _, v := range vs {
		active := " "
		if ep.ActiveVersionID.Valid && ep.ActiveVersionID.Int64 == v.ID {
			active = "*"
		}
		fmt.Printf(" %s v%-4d %s  %-6s %s/%s lang=%s  %s  voices=%d  %s\n", active, v.ID,
			time.Unix(v.CreatedAt, 0).Format("2006-01-02 15:04"), v.Status, v.WhisperModel, v.WhisperBackend,
			v.Language, fmtTime(int64(v.AudioSeconds*1000)), v.NumClusters, v.Timing)
		if v.DiarizeInfo != "" {
			fmt.Printf("         speaker detection: %s\n", v.DiarizeInfo)
		}
		if v.Error != "" {
			fmt.Printf("         error: %s\n", truncate(strings.ReplaceAll(v.Error, "\n", " "), 100))
		}
	}
	return nil
}

func cmdShow(args []string) error {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	verID := fs.Int64("version", 0, "show this version instead of the active one")
	out := fs.String("out", "", "write to this file instead of the console")
	pos := parseInterleaved(fs, args)
	if len(pos) != 1 {
		return fmt.Errorf("usage: qs-podscript show <episode-id> [--version ID] [--out file.txt]")
	}
	id, err := strconv.ParseInt(pos[0], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid episode id %q", pos[0])
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	ep, err := st.Episode(id)
	if err != nil {
		return err
	}
	vid := *verID
	if vid == 0 {
		if !ep.ActiveVersionID.Valid {
			return fmt.Errorf("episode %d has no transcript yet (status: %s)", id, ep.Status)
		}
		vid = ep.ActiveVersionID.Int64
	}
	segs, toks, turns, err := st.LoadResults(vid)
	if err != nil {
		return err
	}
	corr, _ := st.Corrections(vid)
	if len(segs) == 0 {
		return fmt.Errorf("version %d has no transcript data", vid)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n(episode %d, version %d)\n\n", ep.Title, ep.ID, vid)
	for _, u := range applySpelling(buildUtterances(segs, toks, turns, corr), spellingFor(st, ep.FeedID)) {
		fmt.Fprintf(&sb, "[%s] %s: %s\n\n", fmtTime(u.StartMs), labelName(u.Label), u.Text)
	}
	if *out != "" {
		if err := os.WriteFile(*out, []byte(sb.String()), 0o644); err != nil {
			return err
		}
		logf("Written to %s", *out)
		return nil
	}
	fmt.Print(sb.String())
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ------------------------------------------------------------ config

var configKeys = map[string]string{
	"diarize_threshold": "0.5",
	"diarize_model":     defaultDiarizeModel,
	"diarize_step":      "0.1",
	"speaker_device":    deviceAuto,
	"keep_audio":        "1",
	"whisper_context":   "0",
	"whisper_chunks":    "1",
	"voice_merge":       "0",
	"whisper_model":     "turbo",
}

func cmdConfig(args []string) error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	switch len(args) {
	case 0:
		for _, k := range []string{"whisper_model", "whisper_backend", "whisper_release", "diarize_model", "diarize_step", "diarize_threshold", "speaker_device", "voice_merge", "keep_audio", "whisper_chunks", "whisper_context", "whisper_flash_attn"} {
			v := st.Setting(k, configKeys[k])
			if k == "diarize_threshold" {
				m := diarizeOptsFrom(st, 0).Model
				v = fmt.Sprintf("%g (for %s)", defaultThresholdFor(m, st), m)
			}
			fmt.Printf("%-18s %s\n", k, v)
		}
		return nil
	case 2:
		k, v := args[0], args[1]
		switch k {
		case "diarize_threshold":
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f <= 0 || f >= 2 {
				return fmt.Errorf("diarize_threshold must be a number between 0 and 2")
			}
		case "diarize_model":
			if diarizeModels[v] == "" {
				return fmt.Errorf("diarize_model must be one of: %s", strings.Join(diarizeModelKeys(), ", "))
			}
		case "speaker_device":
			if v != deviceAuto && v != deviceCPU {
				return fmt.Errorf("speaker_device must be auto or cpu")
			}
		case "diarize_step":
			if v != "0.1" && v != "0.25" && v != "0.5" {
				return fmt.Errorf("diarize_step must be 0.1, 0.25 or 0.5")
			}
		case "voice_merge":
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f < 0 || f >= 1 {
				return fmt.Errorf("voice_merge must be between 0 and 0.99 (0 = off)")
			}
		case "whisper_chunks", "whisper_context", "whisper_flash_attn":
			if v != "0" && v != "1" {
				return fmt.Errorf("%s must be 0 or 1", k)
			}
		case "keep_audio":
			if v != "0" && v != "1" {
				return fmt.Errorf("keep_audio must be 0 or 1")
			}
		case "whisper_model":
			return fmt.Errorf("change the model with 'qs-podscript setup --model NAME' (it downloads the file)")
		default:
			return fmt.Errorf("unknown or read-only setting %q", k)
		}
		if k == "diarize_threshold" { // stored per voice model
			k = thresholdSetting(diarizeOptsFrom(st, 0).Model)
		}
		if err := st.SetSetting(k, v); err != nil {
			return err
		}
		logf("%s = %s", k, v)
		return nil
	}
	return fmt.Errorf("usage: qs-podscript config [key value]")
}

func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	feedID := fs.Int64("feed", 0, "only this feed")
	fs.Parse(args)
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	checked, broken, err := findBrokenTranscripts(st, *feedID)
	if err != nil {
		return err
	}
	logf("Checked %d transcripts, %d broken (queued again).", checked, broken)
	return nil
}
