package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Paths holds every directory/file location the app uses.
// Everything lives in a "data" folder next to the executable, so the app is
// portable: unzip, run, delete the folder to uninstall.
type Paths struct {
	App    string // folder containing the executable
	Data   string // data/
	Tools  string // data/tools/      (ffmpeg, whisper.cpp)
	Models string // data/models/     (whisper + diarization models)
	Work   string // data/work/       (temporary per-episode files)
	Audio  string // data/audio/      (compact per-version audio for playback/review)
	Images string // data/images/     (small copies of the podcasts' artwork)
	DB     string // data/qs-podscript.db
	Log    string // data/qs-podscript.log
}

var P Paths

func initPaths() error {
	home := os.Getenv("QSPODSCRIPT_HOME")
	if home == "" {
		home = os.Getenv("PODSCRIBE_HOME") // name before the rename
	}
	if home == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("cannot determine executable path: %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		home = filepath.Dir(exe)
	}
	data := filepath.Join(home, "data")
	P = Paths{
		App:    home,
		Data:   data,
		Tools:  filepath.Join(data, "tools"),
		Models: filepath.Join(data, "models"),
		Work:   filepath.Join(data, "work"),
		Audio:  filepath.Join(data, "audio"),
		Images: filepath.Join(data, "images"),
		DB:     filepath.Join(data, "qs-podscript.db"),
		Log:    filepath.Join(data, "qs-podscript.log"),
	}
	for _, d := range []string{P.Data, P.Tools, P.Models, P.Work, P.Audio, P.Images} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("cannot create %s: %w", d, err)
		}
	}
	return migrateOldNames(data)
}

// migrateOldNames renames the database and log from before the rename to
// QS-PodScript (podscribe.db -> qs-podscript.db, same for its -wal/-shm files
// and the log). Only when the new files don't exist yet.
func migrateOldNames(data string) error {
	oldDB := filepath.Join(data, "podscribe.db")
	if fileExists(oldDB) && !fileExists(P.DB) {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if fileExists(oldDB + suffix) {
				if err := os.Rename(oldDB+suffix, P.DB+suffix); err != nil {
					return fmt.Errorf("cannot rename the database to its new name: %w", err)
				}
			}
		}
	}
	oldLog := filepath.Join(data, "podscribe.log")
	if fileExists(oldLog) && !fileExists(P.Log) {
		os.Rename(oldLog, P.Log) // not important if it fails
	}
	return nil
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// ---------------------------------------------------------------------------
// Logging: everything goes to data/qs-podscript.log with timestamps.
// logf also prints to the console; debugf only writes to the file.
// The log file is the thing to send when something goes wrong.
// ---------------------------------------------------------------------------

var (
	logMu   sync.Mutex
	logFile io.Writer = io.Discard
	// set while a \r progress line is on screen, so the next log line starts clean
	progressActive bool
)

func initLogging() {
	f, err := os.OpenFile(P.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot open log file %s: %v\n", P.Log, err)
		return
	}
	logFile = f
	debugf("---- QS-PodScript %s start (%s/%s) args=%q", version, runtime.GOOS, runtime.GOARCH, os.Args[1:])
}

func stamp() string { return time.Now().Format("2006-01-02 15:04:05") }

func logf(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	logMu.Lock()
	defer logMu.Unlock()
	if progressActive {
		fmt.Println()
		progressActive = false
	}
	fmt.Println(msg)
	fmt.Fprintf(logFile, "%s %s\n", stamp(), msg)
	appendLogRing(stamp() + " " + msg)
}

func debugf(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	logMu.Lock()
	defer logMu.Unlock()
	fmt.Fprintf(logFile, "%s [debug] %s\n", stamp(), msg)
}

// progressf overwrites the current console line (no log file entry).
func progressf(format string, a ...any) {
	logMu.Lock()
	defer logMu.Unlock()
	fmt.Printf("\r%-70s", fmt.Sprintf(format, a...))
	progressActive = true
}
