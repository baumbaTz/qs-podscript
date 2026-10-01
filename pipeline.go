package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// processEpisode runs the full chain for one episode and stores a new version:
// download -> ffmpeg (16 kHz mono wav) -> whisper -> diarization -> compact audio.
// On success the new version becomes the episode's active version. Older
// versions are kept for comparison.
func processEpisode(ctx context.Context, st *Store, ep Episode, keepWork bool) (err error) {
	feed, err := st.Feed(ep.FeedID)
	if err != nil {
		return err
	}
	modelName, modelPath := whisperModelPath(st)
	if !fileExists(modelPath) {
		return fmt.Errorf("whisper model missing - run 'qs-podscript setup' first")
	}
	if !fileExists(segmentationModelPath()) {
		return fmt.Errorf("diarization models missing - run 'qs-podscript setup' first")
	}
	if err := ensureSpeakerModels(ctx, st); err != nil { // selected model may have changed
		return err
	}
	dopts := diarizeOptsFrom(st, feed.NumSpeakers)

	logf("")
	logf("== Episode %d: %s (%s)", ep.ID, ep.Title, time.Unix(ep.PubDate, 0).Format("2006-01-02"))
	prevStatus := ep.Status
	claimed, err := st.ClaimEpisode(ep.ID)
	if err != nil {
		return err
	}
	if !claimed {
		return fmt.Errorf("episode %d is already being processed", ep.ID)
	}
	updateStatus(func(s *Status) { s.EpisodeID, s.EpisodeTitle, s.Stage, s.Pct = ep.ID, ep.Title, "Starting", -1 })
	defer updateStatus(func(s *Status) { s.EpisodeID, s.EpisodeTitle = 0, "" })
	v := Version{
		EpisodeID:      ep.ID,
		WhisperModel:   modelName,
		WhisperBackend: installedWhisperBackend(st),
		Language:       feed.Language,
	}
	v.ID, err = st.CreateVersion(v)
	if err != nil {
		return err
	}

	work := filepath.Join(P.Work, fmt.Sprintf("ep%d_v%d", ep.ID, v.ID))
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	var timing []string
	step := func(name string, t0 time.Time) {
		timing = append(timing, fmt.Sprintf("%s %s", name, time.Since(t0).Round(time.Second)))
	}

	defer func() {
		if !keepWork {
			os.RemoveAll(work)
		}
		if err == nil {
			return
		}
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			// interrupted by the user: drop the half-finished version, requeue
			st.DeleteVersion(v.ID)
			back := "new"
			if prevStatus == "queued" || prevStatus == "done" || prevStatus == "error" {
				back = prevStatus
			}
			st.SetEpisodeStatus(ep.ID, back, "")
			logf("   interrupted - episode %d put back as '%s'", ep.ID, back)
			return
		}
		v.Status = "error"
		v.Error = err.Error()
		v.Timing = strings.Join(timing, ", ")
		st.FinishVersion(v)
		st.SetEpisodeStatus(ep.ID, "error", err.Error())
		logf("   ERROR: %v", err)
	}()

	// 1. download
	t0 := time.Now()
	src := filepath.Join(work, "source"+audioExt(ep.AudioURL))
	if err = downloadFile(ctx, ep.AudioURL, src, "episode audio"); err != nil {
		return err
	}
	step("download", t0)

	// 2. convert
	t0 = time.Now()
	setStage("Converting audio", -1)
	wav := filepath.Join(work, "audio.wav")
	if err = runFFmpeg(ctx, "-i", src, "-vn", "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav); err != nil {
		return fmt.Errorf("convert audio: %w", err)
	}
	step("convert", t0)

	// 3. same audio as the previous version? (corrections are carried over by
	//    time, so the timeline must match - dynamic ads can change it)
	var prev Version
	if ep.ActiveVersionID.Valid {
		if pv, e := st.Version(ep.ActiveVersionID.Int64); e == nil && pv.Status == "done" && hasManualCorrections(st, pv.ID) {
			prev = pv
		}
	}
	if prev.ID != 0 {
		if d := wavSeconds(wav) - prev.AudioSeconds; (d > 0.5 || d < -0.5) && prev.AudioFile != "" && fileExists(filepath.Join(P.Audio, prev.AudioFile)) {
			logf("   the downloaded audio differs from the last version by %.1fs (dynamic ads?) - using the saved copy so your corrections still fit", d)
			if err = runFFmpeg(ctx, "-i", filepath.Join(P.Audio, prev.AudioFile), "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav); err != nil {
				return fmt.Errorf("convert saved audio: %w", err)
			}
		}
	}

	// 4.+5. detect speakers, then transcribe (shared with server jobs)
	an, err := analyzeAudio(ctx, st, feed, dopts, modelName, modelPath, wav, work, fmt.Sprintf("ep%d", ep.ID), keepWork)
	if err != nil {
		return err
	}
	samples, turns, embs, segs, toks, device := an.Samples, an.Turns, an.Embs, an.Segs, an.Toks, an.Device
	v.AudioSeconds, v.DiarizeInfo, v.NumClusters = an.AudioSeconds, dopts.String(), an.NumClusters
	timing = append(timing, an.Timing...)
	t0 = time.Now()
	if err = st.SaveTranscription(v.ID, segs, toks); err != nil {
		return err
	}
	v.WhisperBackend = device // what whisper really used
	if device == "cpu" && installedWhisperBackend(st) != "cpu" {
		logf("   WARNING: the %s build of whisper.cpp is installed but it ran on the processor - see the whisper output in the log", installedWhisperBackend(st))
		if b, e := os.ReadFile(filepath.Join(work, "whisper.log")); e == nil {
			debugf("whisper output:\n%s", b)
		}
	}

	// 6. store speakers, carry corrections over, recognize people
	if err = st.SaveDiarization(v.ID, turns, embs); err != nil {
		return err
	}
	runVoiceMerge(st, &v)
	if prev.ID != 0 {
		if n, e := carryCorrections(st, prev, v); e != nil {
			logf("   warning: could not carry corrections over: %v", e)
		} else if n > 0 {
			logf("   carried over %d of your corrections from version %d", n, prev.ID)
		}
	}
	t0 = time.Now()
	runIdentify(ctx, st, &v, samples)
	samples = nil
	step("identify", t0)

	// 7. keep a compact copy of exactly this audio for playback & speaker review
	//    (timestamps only match this file - dynamic ads change the audio per download)
	if st.Setting("keep_audio", "1") == "1" {
		t0 = time.Now()
		setStage("Saving audio copy", -1)
		name := fmt.Sprintf("ep%d_v%d.ogg", ep.ID, v.ID)
		dest := filepath.Join(P.Audio, name)
		if e := runFFmpeg(ctx, "-i", wav, "-c:a", "libopus", "-b:a", "24k", "-ac", "1", dest); ctx.Err() != nil {
			return ctx.Err()
		} else if e != nil {
			logf("   warning: could not save compact audio: %v", e)
		} else {
			v.AudioFile = name
			step("audio", t0)
		}
	}

	v.Status = "done"
	v.Timing = strings.Join(timing, ", ")
	if err = st.FinishVersion(v); err != nil {
		return err
	}
	if err = st.FinishEpisode(ep.ID, v.ID); err != nil {
		return err
	}
	logf("   done: version %d (%s)", v.ID, v.Timing)
	updateStatus(func(s *Status) { s.DoneCount++ })
	return nil
}

func runFFmpeg(ctx context.Context, args ...string) error {
	full := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}, args...)
	out, err := exec.CommandContext(ctx, ffmpegPath(), full...).CombinedOutput()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("ffmpeg: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func audioExt(u string) string {
	if pu, err := url.Parse(u); err == nil {
		ext := strings.ToLower(path.Ext(pu.Path))
		switch ext {
		case ".mp3", ".m4a", ".aac", ".ogg", ".opus", ".wav", ".flac", ".mp4":
			return ext
		}
	}
	return ".mp3"
}

func copyFile(src, dst string) {
	b, err := os.ReadFile(src)
	if err == nil {
		os.WriteFile(dst, b, 0o644)
	}
}

// ------------------------------------------------------------ commands

type queueOpts struct {
	FeedID      int64
	Limit       int
	RetryErrors bool
	Refresh     bool
	KeepWork    bool
	RemoteOnly  bool // only work for the server (automatic helping)
	AsksOnly    bool // only the "asked for" list (asks.go)
}

// runQueue processes episodes until the queue is empty, the limit is reached,
// soft is cancelled (finish current episode, then stop) or hard is cancelled
// (abort now).
func runQueue(hard, soft context.Context, st *Store, o queueOpts) error {
	if n, _ := st.ResetStuck(); n > 0 {
		logf("Requeued %d episode(s) that were interrupted last time", n)
	}
	if o.Refresh {
		setStage("Checking feeds for new episodes", -1)
		if err := refreshFeeds(hard, st, o.FeedID); err != nil {
			logf("warning: feed refresh: %v", err)
		}
	}
	done, failedInRow := 0, 0
	asksOK := o.FeedID == 0 && o.Limit == 0 || o.AsksOnly
	for {
		if hard.Err() != nil || soft.Err() != nil {
			logf("Stopped.")
			return nil
		}
		if o.Limit > 0 && done >= o.Limit {
			logf("Limit of %d episode(s) reached.", o.Limit)
			return nil
		}
		// what was asked for while this computer was busy comes first
		if asksOK {
			did, err, claimErr := processAsk(hard, st, o.KeepWork)
			if claimErr != nil {
				logf("   could not reach the server for an episode asked for there: %v", claimErr)
				updateStatus(func(s *Status) { s.LastError = "Server: " + claimErr.Error() })
				asksOK = false // try again next time
			}
			if did {
				if err != nil {
					if hard.Err() != nil {
						logf("Stopped.")
						return nil
					}
					logf("   %v", err)
					updateStatus(func(s *Status) { s.LastError = err.Error() })
					failedInRow++
					if failedInRow >= 3 {
						return fmt.Errorf("3 episodes failed in a row - stopping (see %s)", P.Log)
					}
					continue
				}
				failedInRow = 0
				done++
				continue
			}
		}
		if o.AsksOnly {
			return nil
		}
		var ep Episode
		ok := false
		if !o.RemoteOnly {
			var err error
			if ep, ok, err = st.NextEpisode(o.FeedID, o.RetryErrors); err != nil {
				return err
			}
		}
		if !ok {
			// own queue empty: transcribe for the server, if connected and allowed
			if o.FeedID == 0 && o.Limit == 0 {
				did, err := processRemoteJob(hard, st)
				if err != nil {
					if hard.Err() != nil {
						logf("Stopped.")
						return nil
					}
					logf("   server job: %v", err)
					updateStatus(func(s *Status) { s.LastError = "Server: " + err.Error() })
					failedInRow++
					if failedInRow >= 3 {
						return fmt.Errorf("3 episodes failed in a row - stopping (see %s)", P.Log)
					}
					continue
				}
				if did {
					failedInRow = 0
					done++
					continue
				}
			}
			if !o.RemoteOnly {
				logf("Queue empty - nothing left to process.")
			}
			return nil
		}
		if err := processQueued(hard, st, ep, o.KeepWork); err != nil {
			if hard.Err() != nil {
				logf("Stopped.")
				return nil
			}
			updateStatus(func(s *Status) { s.LastError = fmt.Sprintf("Episode %d: %v", ep.ID, err) })
			failedInRow++
			if failedInRow >= 3 {
				return fmt.Errorf("3 episodes failed in a row - stopping (see %s)", P.Log)
			}
			continue
		}
		failedInRow = 0
		done++
	}
}

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	feedID := fs.Int64("feed", 0, "only process this feed")
	limit := fs.Int("limit", 0, "stop after N episodes (0 = no limit)")
	retry := fs.Bool("retry-errors", false, "also retry episodes that failed before")
	noRefresh := fs.Bool("no-refresh", false, "don't check feeds for new episodes first")
	keepWork := fs.Bool("keep-work", false, "keep temporary files (for debugging)")
	fs.Parse(args)

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	return runQueue(ctx, softStop, st, queueOpts{
		FeedID: *feedID, Limit: *limit, RetryErrors: *retry, Refresh: !*noRefresh, KeepWork: *keepWork,
	})
}

func cmdProcess(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("process", flag.ExitOnError)
	keepWork := fs.Bool("keep-work", false, "keep temporary files (for debugging)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: qs-podscript process [--keep-work] <episode-id>")
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid episode id %q", fs.Arg(0))
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	st.ResetStuck()
	ep, err := st.Episode(id)
	if err != nil {
		return err
	}
	if ep.ActiveVersionID.Valid {
		logf("Episode %d already has version %d - creating a new version (old one is kept)", id, ep.ActiveVersionID.Int64)
	}
	return processEpisode(ctx, st, ep, *keepWork)
}

// rediarizeVersion redoes only the speaker detection for an existing version,
// using its transcript and its saved audio copy (the exact audio that was
// transcribed - re-downloading could give different ads and timestamps).
// The result is a new version; the source version stays untouched.
func rediarizeVersion(ctx context.Context, st *Store, src Version) (err error) {
	ep, err := st.Episode(src.EpisodeID)
	if err != nil {
		return err
	}
	feed, err := st.Feed(ep.FeedID)
	if err != nil {
		return err
	}
	if src.Status != "done" {
		return fmt.Errorf("version %d is not finished", src.ID)
	}
	audio := filepath.Join(P.Audio, src.AudioFile)
	if src.AudioFile == "" || !fileExists(audio) {
		return fmt.Errorf("version %d has no saved audio copy - use 'Transcribe again' instead", src.ID)
	}
	if !fileExists(segmentationModelPath()) {
		return fmt.Errorf("diarization models missing - run setup first")
	}
	if err := ensureSpeakerModels(ctx, st); err != nil {
		return err
	}
	dopts := diarizeOptsFrom(st, feed.NumSpeakers)

	logf("")
	logf("== Episode %d: redo speaker detection (from version %d)", ep.ID, src.ID)
	prevStatus := ep.Status
	claimed, err := st.ClaimEpisode(ep.ID)
	if err != nil {
		return err
	}
	if !claimed {
		return fmt.Errorf("episode %d is already being processed", ep.ID)
	}
	updateStatus(func(s *Status) { s.EpisodeID, s.EpisodeTitle, s.Stage, s.Pct = ep.ID, ep.Title, "Starting", -1 })
	defer updateStatus(func(s *Status) { s.EpisodeID, s.EpisodeTitle = 0, "" })

	v := Version{
		EpisodeID:      ep.ID,
		WhisperModel:   src.WhisperModel,
		WhisperBackend: src.WhisperBackend,
		Language:       src.Language,
		AudioFile:      src.AudioFile, // same audio, shared file
	}
	v.ID, err = st.CreateVersion(v)
	if err != nil {
		st.SetEpisodeStatus(ep.ID, prevStatus, "")
		return err
	}
	work := filepath.Join(P.Work, fmt.Sprintf("ep%d_v%d", ep.ID, v.ID))
	defer os.RemoveAll(work)
	timing := []string{fmt.Sprintf("transcript from v%d", src.ID)}
	defer func() {
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			st.DeleteVersion(v.ID)
			st.SetEpisodeStatus(ep.ID, prevStatus, "")
			logf("   interrupted")
			return
		}
		v.Status, v.Error, v.Timing = "error", err.Error(), strings.Join(timing, ", ")
		st.FinishVersion(v)
		st.SetEpisodeStatus(ep.ID, prevStatus, "") // the episode itself is still fine
		logf("   ERROR: %v", err)
	}()
	if err = os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	if err = st.CopyTranscription(src.ID, v.ID); err != nil {
		return err
	}

	t0 := time.Now()
	setStage("Converting audio", -1)
	wav := filepath.Join(work, "audio.wav")
	if err = runFFmpeg(ctx, "-i", audio, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav); err != nil {
		return fmt.Errorf("convert audio: %w", err)
	}
	timing = append(timing, "convert "+time.Since(t0).Round(time.Second).String())

	t0 = time.Now()
	setStage("Detecting speakers", -1)
	logf("   speaker detection: %s", dopts)
	samples, err := readWav16k(wav)
	if err != nil {
		return err
	}
	v.AudioSeconds = float64(len(samples)) / sampleRate
	v.DiarizeInfo = dopts.String()
	turns, embs, err := diarizeWav(ctx, wav, dopts)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err = st.SaveDiarization(v.ID, turns, embs); err != nil {
		return err
	}
	clusters := map[int]bool{}
	for _, t := range turns {
		clusters[t.Cluster] = true
	}
	v.NumClusters = len(clusters)
	logf("   speakers: %d voices, %d turns in %s", len(clusters), len(turns), time.Since(t0).Round(time.Second))
	runVoiceMerge(st, &v)
	if n, e := carryCorrections(st, src, v); e != nil {
		logf("   warning: could not carry corrections over: %v", e)
	} else if n > 0 {
		logf("   carried over %d of your corrections from version %d", n, src.ID)
	}
	timing = append(timing, "diarize "+time.Since(t0).Round(time.Second).String())
	t0 = time.Now()
	runIdentify(ctx, st, &v, samples)
	samples = nil
	timing = append(timing, "identify "+time.Since(t0).Round(time.Second).String())

	v.Status, v.Timing = "done", strings.Join(timing, ", ")
	if err = st.FinishVersion(v); err != nil {
		return err
	}
	if err = st.FinishEpisode(ep.ID, v.ID); err != nil {
		return err
	}
	logf("   done: version %d (%s)", v.ID, v.Timing)
	updateStatus(func(s *Status) { s.DoneCount++ })
	return nil
}

func cmdRediarize(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("rediarize", flag.ExitOnError)
	verID := fs.Int64("version", 0, "source version (default: the main version)")
	pos := parseInterleaved(fs, args)
	if len(pos) != 1 {
		return fmt.Errorf("usage: qs-podscript rediarize <episode-id> [--version ID]")
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
			return fmt.Errorf("episode %d has no transcript yet", id)
		}
		vid = ep.ActiveVersionID.Int64
	}
	src, err := st.Version(vid)
	if err != nil {
		return err
	}
	if src.EpisodeID != id {
		return fmt.Errorf("version %d belongs to another episode", vid)
	}
	return rediarizeVersion(ctx, st, src)
}

// wavSeconds: duration of a 16 kHz mono 16-bit wav from its size.
func wavSeconds(path string) float64 {
	st, err := os.Stat(path)
	if err != nil || st.Size() < 44 {
		return 0
	}
	return float64(st.Size()-44) / (sampleRate * 2)
}

// analysis is what analyzeAudio found in one audio file.
type analysis struct {
	Samples      []float32
	Turns        []Turn
	Embs         []ClusterEmbedding
	Segs         []Segment
	Toks         []Token
	Device       string
	AudioSeconds float64
	NumClusters  int
	Timing       []string
}

// analyzeAudio detects the speakers (first: transcription in pieces cuts at
// their turns) and transcribes a 16 kHz wav, with the usual quality checks.
// Used for local episodes and for episodes transcribed for a server.
// label names saved error logs (e.g. "ep12").
func analyzeAudio(ctx context.Context, st *Store, feed Feed, dopts DiarizeOpts, modelName, modelPath, wav, work, label string, keepWork bool) (*analysis, error) {
	an := &analysis{}
	t0 := time.Now()
	logf("   detecting speakers...")
	setStage("Detecting speakers", -1)
	samples, err := readWav16k(wav)
	if err != nil {
		return nil, err
	}
	an.Samples = samples
	an.AudioSeconds = float64(len(samples)) / sampleRate
	logf("   speaker detection: %s", dopts)
	turns, embs, err := diarizeWav(ctx, wav, dopts)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil { // aborted while diarization (uninterruptible C code) ran
		return nil, ctx.Err()
	}
	clusters := map[int]bool{}
	for _, t := range turns {
		clusters[t.Cluster] = true
	}
	an.Turns, an.Embs, an.NumClusters = turns, embs, len(clusters)
	logf("   speakers: %d voices, %d turns in %s", len(clusters), len(turns), time.Since(t0).Round(time.Second))
	an.Timing = append(an.Timing, "diarize "+time.Since(t0).Round(time.Second).String())

	t0 = time.Now()
	if err := ensureVADModel(ctx, feed); err != nil {
		logf("   warning: VAD model: %v - transcribing without VAD", err)
	}
	pieces := whisperChunked(st)
	backend := installedWhisperBackend(st)
	for attempt := 1; ; attempt++ {
		extra := append(whisperExtraArgs(st), vadArgs(feed)...)
		mode := "whole episode"
		if pieces {
			mode = "in pieces"
		}
		logf("   transcribing %s (%s model, %s build, options: %s)...", mode, modelName, backend, strings.Join(extra, " "))
		if pieces {
			an.Device, an.Segs, an.Toks, err = transcribeInPieces(ctx, st, modelPath, samples, turns, feed.Language, work, extra)
		} else {
			outBase := filepath.Join(work, "whisper")
			an.Device, err = runWhisper(ctx, modelPath, wav, outBase, feed.Language, filepath.Join(work, "whisper.log"), extra, func(p int) {
				progressf("   transcribing: %d%%", p)
				setStage("Transcribing", p)
			})
			if err == nil {
				an.Segs, an.Toks, err = parseWhisperJSON(outBase + ".json")
			}
		}
		if err != nil {
			if ctx.Err() != nil || keepWork {
				return nil, err
			}
			// keep whisper's log around - it's what explains the failure
			copyFile(filepath.Join(work, "whisper.log"), filepath.Join(P.Data, "whisper-error-"+label+".log"))
			return nil, fmt.Errorf("%w (whisper log saved as whisper-error-%s.log)", err, label)
		}
		problem := transcriptProblem(an.Segs, an.AudioSeconds)
		if problem == "" {
			break
		}
		copyFile(filepath.Join(work, "whisper.log"), filepath.Join(P.Data, "whisper-garbage-"+label+".log"))
		if attempt == 1 && flashAttn(st) {
			// known GPU problem: garbage output with flash attention
			logf("   transcript unusable (%s) - trying again without flash attention", problem)
			st.SetSetting("whisper_flash_attn", "0")
			setStage("Transcribing again", 0)
			continue
		}
		return nil, fmt.Errorf("transcript unusable (%s) - whisper produces garbage on this computer; see whisper-garbage-%s.log and run the self-check (Setup page)", problem, label)
	}
	an.Timing = append(an.Timing, "whisper "+time.Since(t0).Round(time.Second).String())
	logf("   transcribed: %d segments in %s on %s", len(an.Segs), time.Since(t0).Round(time.Second), deviceText(an.Device))
	return an, nil
}
