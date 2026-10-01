package main

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// What QS-PodScript is built from, with the versions actually in use on this
// computer - shown in the manual ("What QS-PodScript uses").

type toolInfo struct {
	Name    string
	Purpose string
	Version string
	Link    string
}

var ffmpegVer struct {
	sync.Mutex
	path string
	mod  time.Time
	line string
}

// ffmpegVersion asks ffmpeg itself (cached until the file changes).
func ffmpegVersion() string {
	p := ffmpegPath()
	fi, err := os.Stat(p)
	if err != nil {
		if lp, lerr := exec.LookPath(p); lerr == nil {
			p = lp
			fi, err = os.Stat(p)
		}
	}
	if err != nil {
		return "not installed"
	}
	ffmpegVer.Lock()
	defer ffmpegVer.Unlock()
	if ffmpegVer.path == p && ffmpegVer.mod.Equal(fi.ModTime()) {
		return ffmpegVer.line
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p, "-hide_banner", "-version")
	out, err := cmd.Output()
	line := "installed (version unknown)"
	if err == nil {
		// "ffmpeg version 7.1.1 Copyright ..." -> "7.1.1"
		f := strings.Fields(firstLine(string(out)))
		if len(f) >= 3 && f[1] == "version" {
			line = f[2]
		}
	}
	ffmpegVer.path, ffmpegVer.mod, ffmpegVer.line = p, fi.ModTime(), line
	return line
}

func backendText(b string) string {
	switch b {
	case "cuda":
		return "graphics card, NVIDIA CUDA"
	case "vulkan":
		return "graphics card, Vulkan"
	}
	return "processor"
}

func toolVersions(st *Store) []toolInfo {
	s := getSetupState(st)
	whisper := "not installed"
	if s.WhisperOK {
		whisper = st.Setting("whisper_release", whisperRelease) + " – " + backendText(s.Backend)
	}
	model := s.Model
	if name, _ := whisperModelPath(st); whisperModels[name] != "" {
		model = name + " (" + whisperModels[name] + ")"
	}
	if !s.ModelOK {
		model += " – not downloaded"
	}
	det := diarizeOptsFrom(st, 0).Model
	detModel := det + " (" + diarizeModels[det] + ")"
	detLink := "https://github.com/wenet-e2e/wespeaker"
	switch {
	case strings.HasPrefix(det, "titanet"):
		detLink = "https://github.com/NVIDIA/NeMo"
	case det == "eres2net" || det == "campplus-3d":
		detLink = "https://github.com/modelscope/3D-Speaker"
	}
	sqliteVer := ""
	st.db.QueryRow(`SELECT sqlite_version()`).Scan(&sqliteVer)
	vad := "not downloaded (only needed when a podcast uses it)"
	if fileExists(vadModelPath()) {
		vad = vadModelFile
	}

	return []toolInfo{
		{"QS-PodScript", "this app: podcasts, queue, speakers, corrections, web pages", version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ", built with " + runtime.Version() + ")", "https://github.com/baumbatz/qs-podscript"},
		{"whisper.cpp", "speech engine: turns the audio into text", whisper, "https://github.com/ggml-org/whisper.cpp"},
		{"Whisper speech model", "OpenAI's Whisper model the speech engine runs", model, "https://github.com/openai/whisper"},
		{"FFmpeg", "reads the episode audio and makes the small copy for listening", ffmpegVersion(), "https://ffmpeg.org"},
		{"sherpa-onnx", "speaker detection: tells voices apart", sherpa.GetVersion() + " (ONNX Runtime " + sherpa.GetOnnxruntimeVersion() + ")", "https://github.com/k2-fsa/sherpa-onnx"},
		{"pyannote segmentation 3.0", "finds where one speaker stops and another starts", "segmentation-3.0", "https://huggingface.co/pyannote/segmentation-3.0"},
		{"Voice model for detection", "groups speech into voices (setting on the Setup page)", detModel, detLink},
		{"Speaker detection runs on", "graphics card support is optional (Linux installer: --gpu-speakers)", deviceSummary(), ""},
		{"Voice model for voiceprints", "voice samples of people – fixed, so voiceprints stay comparable", embeddingFile, "https://github.com/wenet-e2e/wespeaker"},
		{"Silero VAD", "voice activity detection, only for podcasts where it's switched on", vad, "https://github.com/snakers4/silero-vad"},
		{"SQLite", "the database with transcripts, people and settings", sqliteVer, "https://sqlite.org"},
	}
}
