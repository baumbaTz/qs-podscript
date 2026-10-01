package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Pinned tool versions. Bump deliberately, never "latest", so a working
// setup stays reproducible.
const (
	whisperRelease = "v1.9.2"
	whisperBaseURL = "https://github.com/ggml-org/whisper.cpp/releases/download/" + whisperRelease + "/"
	hfWhisperBase  = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/"

	ffmpegWinURL = "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip"

	segmentationURL  = "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-segmentation-models/sherpa-onnx-pyannote-segmentation-3-0.tar.bz2"
	speakerModelsURL = "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/"
	// The voiceprint model is FIXED: stored voiceprints must stay comparable
	// across all episodes, whatever detection model the user picks.
	embeddingFile = "wespeaker_en_voxceleb_resnet34_LM.onnx"
	embeddingURL  = speakerModelsURL + embeddingFile

	// CUDA 12.4 builds need at least this NVIDIA driver on Windows.
	minCudaDriver = 551.61
)

var whisperModels = map[string]string{
	"turbo":     "ggml-large-v3-turbo.bin",      // ~1.6 GB, best speed/quality on GPU
	"turbo-q5":  "ggml-large-v3-turbo-q5_0.bin", // ~550 MB, nearly same quality
	"large-v3":  "ggml-large-v3.bin",            // ~3 GB, slowest, marginally better
	"medium.en": "ggml-medium.en.bin",
	"small.en":  "ggml-small.en.bin",
	"base.en":   "ggml-base.en.bin", // tiny/fast, for testing only
}

// Voice models usable for speaker detection (clustering). Selectable.
// Threshold: default separation threshold for that model - the models
// measure "how different" on different scales, so each needs its own.
// Real audio: ResNet34 0.5 and TitaNet large 0.96 (Batz). The others are
// estimates (v0.27.3) from the synthetic sweep, moved towards fewer voices
// the way real audio did for TitaNet: big ResNets 0.2 split, 0.5 merged
// everyone -> 0.3; TitaNet small like large -> 0.9; ERes2Net 0.7 was
// better than 0.6; CAM++ 0.5 gave 4x too many voices -> 0.7.
// Speed: measured on the CPU relative to ResNet34 (higher = slower).
type diarizeModelInfo struct {
	Key, File, Label string
	Threshold        float32
	Speed            float32
}

var diarizeModelList = []diarizeModelInfo{
	{"resnet34", "wespeaker_en_voxceleb_resnet34_LM.onnx", "ResNet34 (WeSpeaker) – the tried default", 0.5, 1},
	{"resnet152", "wespeaker_en_voxceleb_resnet152_LM.onnx", "ResNet152 (WeSpeaker) – bigger", 0.3, 2.9},
	{"resnet221", "wespeaker_en_voxceleb_resnet221_LM.onnx", "ResNet221 (WeSpeaker) – bigger still", 0.3, 4.0},
	{"resnet293", "wespeaker_en_voxceleb_resnet293_LM.onnx", "ResNet293 (WeSpeaker) – the biggest", 0.3, 5.1},
	{"titanet", "nemo_en_titanet_small.onnx", "TitaNet small (NVIDIA NeMo)", 0.9, 0.6},
	{"titanet-large", "nemo_en_titanet_large.onnx", "TitaNet large (NVIDIA NeMo)", 0.96, 1.2},
	{"eres2net", "3dspeaker_speech_eres2net_sv_en_voxceleb_16k.onnx", "ERes2Net (3D-Speaker, Alibaba)", 0.7, 1.15},
	{"campplus", "wespeaker_en_voxceleb_CAM++_LM.onnx", "CAM++ (WeSpeaker)", 0.7, 0.68},
	{"campplus-3d", "3dspeaker_speech_campplus_sv_en_voxceleb_16k.onnx", "CAM++ (3D-Speaker, Alibaba)", 0.7, 0.62},
}

var diarizeModels = func() map[string]string {
	m := map[string]string{}
	for _, d := range diarizeModelList {
		m[d.Key] = d.File
	}
	return m
}()

func diarizeModelKeys() []string {
	var k []string
	for _, d := range diarizeModelList {
		k = append(k, d.Key)
	}
	return k
}

func diarizeModelByKey(key string) diarizeModelInfo {
	for _, d := range diarizeModelList {
		if d.Key == key {
			return d
		}
	}
	return diarizeModelList[0]
}

// thresholdSetting: resnet34 keeps the old setting name (existing installs).
func thresholdSetting(model string) string {
	if model == "resnet34" {
		return "diarize_threshold"
	}
	return "diarize_threshold_" + model
}

// defaultThresholdFor: the stored threshold of that model, else its default.
func defaultThresholdFor(model string, st *Store) float32 {
	if t, err := strconv.ParseFloat(st.Setting(thresholdSetting(model), ""), 32); err == nil && t > 0 {
		return float32(t)
	}
	return diarizeModelByKey(model).Threshold
}

const (
	defaultDiarizeModel = "resnet34"
	defaultDiarizeStep  = float32(0.1)
)

var diarizeSteps = []float32{0.1, 0.25, 0.5}

func diarizeModelPath(key string) string {
	f, ok := diarizeModels[key]
	if !ok {
		f = diarizeModels[defaultDiarizeModel]
	}
	return filepath.Join(P.Models, f)
}

// diarizeOptsFrom reads the current speaker detection settings.
func diarizeOptsFrom(st *Store, numSpeakers int) DiarizeOpts {
	o := DiarizeOpts{NumSpeakers: numSpeakers, Model: defaultDiarizeModel, Step: defaultDiarizeStep}
	if m := st.Setting("diarize_model", ""); diarizeModels[m] != "" {
		o.Model = m
	}
	o.Threshold = defaultThresholdFor(o.Model, st)
	if s, err := strconv.ParseFloat(st.Setting("diarize_step", ""), 32); err == nil {
		for _, v := range diarizeSteps {
			if float32(s) == v {
				o.Step = v
			}
		}
	}
	return o
}

// voiceMergeSimilarity: minimum voiceprint similarity to merge two voices (0 = off).
func voiceMergeSimilarity(st *Store) float64 {
	if v, err := strconv.ParseFloat(st.Setting("voice_merge", ""), 64); err == nil && v >= 0 && v < 1 {
		return v
	}
	return defaultVoiceMergeSimilarity
}

// runVoiceMerge applies the merge pass to a version and records the result.
func runVoiceMerge(st *Store, v *Version) {
	sim := voiceMergeSimilarity(st)
	n, err := applyVoiceMerge(st, v.ID, sim)
	if err != nil {
		logf("   warning: voice merging failed: %v", err)
		return
	}
	if sim > 0 {
		logf("   merged similar voices (similarity >= %.2f): %d voices -> %d", sim, v.NumClusters, v.NumClusters-n)
		v.DiarizeInfo += fmt.Sprintf(", voices merged at %.2f: %d -> %d", sim, v.NumClusters, v.NumClusters-n)
	}
}

// ensureSpeakerModels downloads the fixed voiceprint model and the selected
// detection model if they are missing.
func ensureSpeakerModels(ctx context.Context, st *Store) error {
	if !fileExists(embeddingModelPath()) {
		logf("Downloading voiceprint model")
		if err := downloadFile(ctx, embeddingURL, embeddingModelPath(), "voiceprint model"); err != nil {
			return err
		}
	}
	key := diarizeOptsFrom(st, 0).Model
	if p := diarizeModelPath(key); !fileExists(p) {
		logf("Downloading speaker detection model %s", key)
		if err := downloadFile(ctx, speakerModelsURL+filepath.Base(p), p, "speaker model "+key); err != nil {
			return err
		}
	}
	return nil
}

func modelNames() string {
	return "turbo, turbo-q5, large-v3, medium.en, small.en, base.en"
}

// ------------------------------------------------------------ tool locations

func whisperDir() string { return filepath.Join(P.Tools, "whisper") }

// customWhisperBackend returns "cuda"/"vulkan" if install.sh built whisper.cpp
// for the GPU (marker file BUILD_INFO next to whisper-cli), else "".
//
// If BUILD_INFO carries the sha256 of the binary it was written for and the
// current whisper-cli differs (e.g. replaced by copying another computer's
// data folder), the marker is ignored: QS-PodScript must not claim a GPU build
// that isn't there.
func customWhisperBackend() string {
	b, err := os.ReadFile(filepath.Join(whisperDir(), "BUILD_INFO"))
	if err != nil {
		return ""
	}
	backend, sum := "", ""
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if v, ok := strings.CutPrefix(l, "backend="); ok {
			backend = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(l, "sha256="); ok {
			sum = strings.TrimSpace(v)
		}
	}
	if backend != "" && sum != "" {
		if got, err := fileSHA256(filepath.Join(whisperDir(), exeName("whisper-cli"))); err != nil || got != sum {
			debugf("BUILD_INFO says %s build, but whisper-cli changed - ignoring the marker", backend)
			return ""
		}
	}
	return backend
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func whisperCliPath() string {
	p := filepath.Join(whisperDir(), exeName("whisper-cli"))
	if fileExists(p) {
		return p
	}
	if runtime.GOOS != "windows" {
		if lp := lookTool("whisper-cli"); lp != "" {
			return lp
		}
	}
	return p
}

// lookTool finds a program on the PATH - on macOS also in Homebrew's folders,
// which are missing from the PATH when the app is started from the Finder.
func lookTool(name string) string {
	if lp, err := exec.LookPath(name); err == nil {
		return lp
	}
	if runtime.GOOS == "darwin" {
		for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
			if p := filepath.Join(dir, name); fileExists(p) {
				return p
			}
		}
	}
	return ""
}

func ffmpegPath() string {
	p := filepath.Join(P.Tools, "ffmpeg", exeName("ffmpeg"))
	if fileExists(p) {
		return p
	}
	if lp := lookTool("ffmpeg"); lp != "" {
		return lp
	}
	return p
}

func whisperModelPath(st *Store) (string, string) {
	name := st.Setting("whisper_model", "turbo")
	file, ok := whisperModels[name]
	if !ok {
		file = whisperModels["turbo"]
	}
	return name, filepath.Join(P.Models, file)
}

func segmentationModelPath() string {
	return filepath.Join(P.Models, "segmentation", "model.onnx")
}

func embeddingModelPath() string { return filepath.Join(P.Models, embeddingFile) }

// whisperCmd prepares a whisper-cli command running inside workDir.
// Callers pass paths relative to workDir (see relPath): whisper-cli on Windows
// may not cope with non-ASCII characters in absolute paths (C:\Users\Jörg\...),
// while our own data sub-paths are always plain ASCII.
func whisperCmd(ctx context.Context, workDir string, args ...string) *exec.Cmd {
	cli := whisperCliPath()
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Dir = workDir
	if runtime.GOOS == "linux" {
		cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+filepath.Dir(cli)+":"+os.Getenv("LD_LIBRARY_PATH"))
	}
	return cmd
}

// installedWhisperBackend: which whisper.cpp build is installed. A local GPU
// build (BUILD_INFO) wins over the stored setting, which may be stale e.g.
// after copying a database from another computer.
func installedWhisperBackend(st *Store) string {
	if c := customWhisperBackend(); c != "" {
		return c
	}
	return st.Setting("whisper_backend", "cpu")
}

func deviceText(d string) string {
	switch d {
	case "cuda":
		return "the graphics card (CUDA)"
	case "vulkan":
		return "the graphics card (Vulkan)"
	case "metal":
		return "the Apple GPU (Metal)"
	}
	return "the processor"
}

// ------------------------------------------------------------ GPU detection

type gpuInfo struct {
	Name   string
	Driver string
}

func detectNvidia(ctx context.Context) (gpuInfo, bool) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	smi := "nvidia-smi"
	if runtime.GOOS == "windows" {
		if p := filepath.Join(os.Getenv("SystemRoot"), "System32", "nvidia-smi.exe"); fileExists(p) {
			smi = p
		}
	}
	out, err := exec.CommandContext(ctx, smi, "--query-gpu=name,driver_version", "--format=csv,noheader").Output()
	if err != nil {
		debugf("nvidia-smi not usable: %v", err)
		return gpuInfo{}, false
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	parts := strings.Split(line, ",")
	if len(parts) < 2 {
		return gpuInfo{}, false
	}
	return gpuInfo{Name: strings.TrimSpace(parts[0]), Driver: strings.TrimSpace(parts[1])}, true
}

// ------------------------------------------------------------ setup command

func cmdSetup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	model := fs.String("model", "", "whisper model: "+modelNames())
	cpu := fs.Bool("cpu", false, "same as --gpu cpu")
	gpu := fs.String("gpu", "", "auto (default), cuda, vulkan or cpu")
	force := fs.Bool("force", false, "re-download tools even if present")
	fs.Parse(args)

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	mode := *gpu
	if mode == "" {
		mode = st.Setting("gpu_mode", gpuAuto)
	}
	if *cpu {
		mode = gpuCPU
	}
	if !validGPUMode(mode) {
		return fmt.Errorf("--gpu must be auto, cuda, vulkan or cpu")
	}
	return runSetup(ctx, st, *model, mode, *force)
}

// GPU modes for setup: "auto" picks the best available and falls back if a
// GPU version doesn't actually run on the GPU.
const (
	gpuAuto   = "auto"
	gpuCUDA   = "cuda"
	gpuVulkan = "vulkan"
	gpuCPU    = "cpu"
)

func validGPUMode(m string) bool {
	return m == gpuAuto || m == gpuCUDA || m == gpuVulkan || m == gpuCPU
}

func runSetup(ctx context.Context, st *Store, modelArg string, gpuMode string, forceArg bool) error {
	model, force := &modelArg, &forceArg
	if !validGPUMode(gpuMode) {
		gpuMode = gpuAuto
	}
	logf("Setting up QS-PodScript in %s", P.Data)
	st.SetSetting("gpu_mode", gpuMode)

	// ---- ffmpeg
	if runtime.GOOS == "windows" {
		ff := filepath.Join(P.Tools, "ffmpeg", "ffmpeg.exe")
		if *force || !fileExists(ff) {
			if err := installFFmpegWindows(ctx); err != nil {
				return err
			}
		} else {
			logf("ffmpeg already installed")
		}
	} else if lookTool("ffmpeg") == "" {
		if runtime.GOOS == "darwin" {
			return fmt.Errorf("ffmpeg not found - install it with Homebrew: brew install ffmpeg whisper-cpp")
		}
		return fmt.Errorf("ffmpeg not found in PATH - install it with your package manager (e.g. 'sudo apt install ffmpeg')")
	} else {
		logf("Using system ffmpeg")
	}

	// ---- whisper model
	name := *model
	if name == "" {
		name = st.Setting("whisper_model", "turbo")
	}
	file, ok := whisperModels[name]
	if !ok {
		return fmt.Errorf("unknown model %q (choose: %s)", name, modelNames())
	}
	mp := filepath.Join(P.Models, file)
	if !fileExists(mp) {
		logf("Downloading whisper model %s", name)
		if err := downloadFile(ctx, hfWhisperBase+file, mp, file); err != nil {
			return err
		}
	} else {
		logf("Whisper model %s already present", name)
	}
	st.SetSetting("whisper_model", name)

	// ---- diarization models
	if !fileExists(segmentationModelPath()) {
		logf("Downloading speaker segmentation model")
		arc := filepath.Join(P.Models, "segmentation.tar.bz2")
		if err := downloadFile(ctx, segmentationURL, arc, "segmentation model"); err != nil {
			return err
		}
		n, err := extractTar(arc, filepath.Join(P.Models, "segmentation"), func(name string) string {
			if filepath.Base(name) == "model.onnx" {
				return "model.onnx"
			}
			return ""
		})
		os.Remove(arc)
		if err != nil || n == 0 {
			return fmt.Errorf("extract segmentation model: %v (files: %d)", err, n)
		}
	} else {
		logf("Speaker segmentation model already present")
	}
	if err := ensureSpeakerModels(ctx, st); err != nil {
		return err
	}

	// ---- whisper.cpp (after ffmpeg + model: each GPU option is tested for real)
	setStage("Checking graphics card", -1)
	if err := setupWhisper(ctx, st, gpuMode, *force); err != nil {
		return err
	}

	logf("Setup complete. Running self-check...")
	setStage("Running self-check", -1)
	err := runCheck(ctx, st)
	st.SetSetting("setup_check", map[bool]string{true: "ok", false: "failed"}[err == nil])
	return err
}

func installWhisper(ctx context.Context, backend string) error {
	if backend == gpuVulkan {
		if runtime.GOOS == "windows" && bundledVulkanWhisper() {
			return installBundledVulkan()
		}
		return fmt.Errorf("no Vulkan version of whisper.cpp available here")
	}
	var asset string
	switch runtime.GOOS {
	case "windows":
		if backend == "cuda" {
			asset = "whisper-cublas-12.4.0-bin-x64.zip" // ~670 MB, includes CUDA runtime DLLs
		} else {
			asset = "whisper-bin-x64.zip"
		}
	case "linux":
		asset = "whisper-bin-ubuntu-x64.tar.gz"
	default:
		return fmt.Errorf("automatic whisper.cpp install not supported on %s yet", runtime.GOOS)
	}
	logf("Downloading whisper.cpp %s (%s)", whisperRelease, backend)
	dl := filepath.Join(P.Tools, asset)
	if err := downloadFile(ctx, whisperBaseURL+asset, dl, asset); err != nil {
		return err
	}
	defer os.Remove(dl)

	os.RemoveAll(whisperDir())
	if err := os.MkdirAll(whisperDir(), 0o755); err != nil {
		return err
	}
	setStage("Extracting whisper.cpp", -1)
	filter := func(name string) string {
		rel := stripFirstDir(name)
		base := filepath.Base(rel)
		if rel == "" || strings.HasPrefix(base, "test-") {
			return ""
		}
		return base // flatten: everything in one folder next to whisper-cli
	}
	logf("Extracting %s ...", asset)
	var n int
	var err error
	if strings.HasSuffix(asset, ".zip") {
		n, err = extractZip(dl, whisperDir(), filter)
	} else {
		n, err = extractTar(dl, whisperDir(), filter)
	}
	if err != nil {
		return fmt.Errorf("extract %s: %w", asset, err)
	}
	if !fileExists(filepath.Join(whisperDir(), exeName("whisper-cli"))) {
		return fmt.Errorf("whisper-cli not found after extracting %s (%d files)", asset, n)
	}
	logf("whisper.cpp installed (%d files)", n)
	return nil
}

func installFFmpegWindows(ctx context.Context) error {
	logf("Downloading ffmpeg")
	dl := filepath.Join(P.Tools, "ffmpeg.zip")
	if err := downloadFile(ctx, ffmpegWinURL, dl, "ffmpeg"); err != nil {
		return err
	}
	defer os.Remove(dl)
	n, err := extractZip(dl, filepath.Join(P.Tools, "ffmpeg"), func(name string) string {
		if strings.EqualFold(filepath.Base(name), "ffmpeg.exe") {
			return "ffmpeg.exe"
		}
		return ""
	})
	if err != nil || n == 0 {
		return fmt.Errorf("extract ffmpeg: %v (files: %d)", err, n)
	}
	logf("ffmpeg installed")
	return nil
}

// ------------------------------------------------------------ check command

func cmdCheck(ctx context.Context, args []string) error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	err = runCheck(ctx, st)
	st.SetSetting("setup_check", map[bool]string{true: "ok", false: "failed"}[err == nil])
	return err
}

func runCheck(ctx context.Context, st *Store) error {
	ok := true
	fail := func(format string, a ...any) {
		ok = false
		logf("  FAIL  "+format, a...)
	}

	// ffmpeg
	ff := ffmpegPath()
	out, err := exec.CommandContext(ctx, ff, "-hide_banner", "-version").Output()
	if err != nil {
		fail("ffmpeg (%s): %v", ff, err)
	} else {
		logf("  ok    ffmpeg: %s", firstLine(string(out)))
	}

	// whisper: transcribe 2 seconds of generated audio and look at what device it used
	name, mp := whisperModelPath(st)
	if !fileExists(mp) {
		fail("whisper model %s missing (%s) - run setup", name, mp)
	} else if !fileExists(whisperCliPath()) {
		fail("whisper-cli missing (%s) - run setup", whisperCliPath())
	} else if dev, textOK, err := verifyWhisperOutput(ctx, st); err != nil {
		fail("%v (details in log file)", err)
	} else if !textOK {
		fail("whisper ran on %s but produced garbage instead of text, also without flash attention (details in log file)", dev)
	} else {
		fa := ""
		if !flashAttn(st) {
			fa = ", without flash attention"
		}
		logf("  ok    whisper (%s model%s, speech test passed): %s", name, fa, dev)
		if b := installedWhisperBackend(st); (b == gpuCUDA || b == gpuVulkan) && strings.HasPrefix(dev, "CPU") {
			logf("  WARN  %s build installed but no GPU device reported - check the log file", b)
		}
	}

	// diarization models
	if !fileExists(segmentationModelPath()) || !fileExists(embeddingModelPath()) {
		fail("diarization models missing - run setup")
	} else if err := checkDiarizationModels(); err != nil {
		fail("diarization: %v", err)
	} else {
		logf("  ok    speaker diarization models load")
	}

	if !ok {
		return fmt.Errorf("self-check failed (see %s)", P.Log)
	}
	logf("All checks passed.")
	return nil
}

// whisperDeviceSummary extracts which compute device whisper/ggml reported.
func whisperDeviceSummary(out string) string {
	var cuda, vulkan []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		ll := strings.ToLower(l)
		switch {
		// "device 0: CPU (type: 0)" is printed on CPU-only builds too - only
		// lines that name a CUDA / Vulkan device count
		case strings.Contains(ll, "ggml_cuda_init") || strings.Contains(ll, "cuda0") ||
			(strings.Contains(ll, "device 0:") && strings.Contains(ll, "compute capability")):
			cuda = append(cuda, l)
		case strings.Contains(ll, "ggml_vulkan: 0 =") || (strings.Contains(ll, "found") && strings.Contains(ll, "vulkan devices")) ||
			strings.Contains(ll, "vulkan0"):
			vulkan = append(vulkan, l)
		}
	}
	if len(cuda) > 0 {
		return "CUDA - " + strings.Join(cuda, " | ")
	}
	if len(vulkan) > 0 {
		return "Vulkan - " + strings.Join(vulkan, " | ")
	}
	return "CPU"
}

// relPath returns target relative to base, or target itself if that fails.
func relPath(base, target string) string {
	if r, err := filepath.Rel(base, target); err == nil {
		return r
	}
	return target
}

func firstLine(s string) string {
	return strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
}

// SetupState summarizes what is installed, for the web UI.
type SetupState struct {
	WhisperOK, FFmpegOK, ModelOK, DiarOK bool
	Backend, Model, GPU, Check           string
}

func (s SetupState) Ready() bool { return s.WhisperOK && s.FFmpegOK && s.ModelOK && s.DiarOK }

func getSetupState(st *Store) SetupState {
	name, mp := whisperModelPath(st)
	_, ffErr := os.Stat(ffmpegPath())
	return SetupState{
		WhisperOK: fileExists(whisperCliPath()),
		FFmpegOK:  ffErr == nil,
		ModelOK:   fileExists(mp),
		DiarOK:    fileExists(segmentationModelPath()) && fileExists(embeddingModelPath()),
		Backend:   installedWhisperBackend(st),
		Model:     name,
		GPU:       st.Setting("gpu_name", ""),
		Check:     st.Setting("setup_check", ""),
	}
}

// ------------------------------------------------------------ whisper backend choice

// gpuNames lists the graphics adapters (Windows: WMI; Linux: NVIDIA only).
func gpuNames(ctx context.Context) []string {
	if runtime.GOOS != "windows" {
		if g, ok := detectNvidia(ctx); ok {
			return []string{g.Name}
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command",
		"(Get-CimInstance Win32_VideoController).Name").Output()
	if err != nil {
		debugf("could not list graphics adapters: %v", err)
		return nil
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names
}

// vulkanCapable: a GPU that is worth running whisper on via Vulkan.
// Intel integrated graphics are usually not faster than the processor.
func vulkanCapable(name string) bool {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "nvidia"), strings.Contains(n, "radeon"), strings.Contains(n, "amd"):
		return !strings.Contains(n, "radeon(tm) graphics") // AMD APU integrated graphics
	case strings.Contains(n, "intel") && strings.Contains(n, "arc"):
		return true
	}
	return false
}

func vulkanRuntimePresent() bool {
	if runtime.GOOS != "windows" {
		return true
	}
	return fileExists(filepath.Join(os.Getenv("SystemRoot"), "System32", "vulkan-1.dll"))
}

// setupWhisper installs whisper.cpp for the chosen GPU mode and verifies with
// a real test run that it uses the GPU; otherwise it falls back
// (CUDA -> Vulkan -> processor).
func setupWhisper(ctx context.Context, st *Store, mode string, force bool) error {
	if runtime.GOOS == "darwin" {
		// macOS: whisper.cpp from Homebrew (built with Metal: uses the GPU of
		// Apple Silicon by itself)
		lp := lookTool("whisper-cli")
		if lp == "" {
			return fmt.Errorf("whisper.cpp not found - install it with Homebrew: brew install whisper-cpp ffmpeg")
		}
		logf("Using whisper.cpp from Homebrew: %s", lp)
		backend := "metal"
		if runtime.GOARCH != "arm64" {
			backend = "cpu"
		}
		st.SetSetting("whisper_backend", backend)
		st.SetSetting("whisper_release", whisperRelease)
		st.SetSetting("gpu_name", map[bool]string{true: "Apple Silicon (Metal)", false: ""}[backend == "metal"])
		return nil
	}
	if runtime.GOOS != "windows" {
		// Linux: GPU versions are built by install.sh; setup keeps them
		if g, ok := detectNvidia(ctx); ok {
			st.SetSetting("gpu_name", g.Name+" (driver "+g.Driver+")")
		} else {
			st.SetSetting("gpu_name", "")
		}
		if custom := customWhisperBackend(); custom != "" && !force {
			logf("Keeping the %s build of whisper.cpp made by the install script", custom)
			st.SetSetting("whisper_backend", custom)
			st.SetSetting("whisper_release", whisperRelease)
			if fileExists(whisperModelPathOnly(st)) {
				setStage("Testing transcription", -1)
				if dev, ok, err := verifyWhisperOutput(ctx, st); err == nil && !ok {
					logf("WARNING: the %s build produces garbage instead of text (%s), even without flash attention.", custom, dev)
					logf("         Run install.sh again, or choose 'Processor only' on the Setup page.")
				}
			}
			return nil
		}
		if mode != gpuCPU {
			logf("Linux: using the CPU build of whisper.cpp. For GPU speed run install.sh (builds a CUDA/Vulkan version).")
		}
		return ensureWhisper(ctx, st, gpuCPU, force)
	}

	names := gpuNames(ctx)
	nv, nvOK := detectNvidia(ctx)
	var display []string
	for _, n := range names {
		if nvOK && strings.Contains(strings.ToLower(n), "nvidia") {
			n += " (driver " + nv.Driver + ")"
		}
		display = append(display, n)
	}
	st.SetSetting("gpu_name", strings.Join(display, ", "))
	if len(display) > 0 {
		logf("Graphics: %s", strings.Join(display, ", "))
	}
	vk := false
	for _, n := range names {
		if vulkanCapable(n) {
			vk = true
		}
	}
	if vk && !vulkanRuntimePresent() {
		logf("Vulkan runtime (vulkan-1.dll) not found - update the graphics driver to use the Vulkan version.")
		vk = false
	}
	cudaOK := nvOK
	if nvOK {
		if d, err := strconv.ParseFloat(nv.Driver, 64); err == nil && d < minCudaDriver {
			logf("NVIDIA driver %s is older than %.2f, which the CUDA version needs - update the driver for full speed.", nv.Driver, minCudaDriver)
			cudaOK = false
		}
	}

	var order []string
	switch mode {
	case gpuCUDA:
		order = []string{gpuCUDA, gpuVulkan, gpuCPU}
	case gpuVulkan:
		order = []string{gpuVulkan, gpuCPU}
	case gpuCPU:
		order = []string{gpuCPU}
	default: // auto
		if cudaOK {
			order = append(order, gpuCUDA)
		}
		if vk {
			order = append(order, gpuVulkan)
		}
		order = append(order, gpuCPU)
	}

	for i, b := range order {
		if b == gpuVulkan && !bundledVulkanWhisper() {
			logf("The Vulkan version of whisper.cpp is not included in this package - skipping it.")
			continue
		}
		if err := ensureWhisper(ctx, st, b, force); err != nil {
			if i == len(order)-1 {
				return err
			}
			logf("Could not install the %s version: %v - trying the next option.", b, err)
			continue
		}
		if !fileExists(whisperModelPathOnly(st)) {
			return nil // no model yet (setup run without model) - can't test
		}
		setStage("Testing transcription", -1)
		dev, textOK, err := verifyWhisperOutput(ctx, st)
		if b == gpuCPU {
			if err == nil && !textOK {
				logf("WARNING: even the processor version produced no proper text in the test - check the log.")
			}
			logf("Transcription will run on the processor.")
			return nil
		}
		if err == nil && textOK && strings.HasPrefix(strings.ToLower(dev), b) {
			logf("Transcription uses the graphics card (%s).", strings.ToUpper(b[:1])+b[1:])
			return nil
		}
		reason := firstNonEmpty(errString(err), dev)
		if err == nil && !textOK {
			reason = "it produced garbage instead of text"
		}
		logf("The %s version can't be used (%s) - trying the next option.", b, reason)
		force = true // next option must really be installed
	}
	return nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

func whisperModelPathOnly(st *Store) string { _, p := whisperModelPath(st); return p }

// ensureWhisper installs the given whisper.cpp build unless it's already there.
func ensureWhisper(ctx context.Context, st *Store, backend string, force bool) error {
	need := force || !fileExists(filepath.Join(whisperDir(), exeName("whisper-cli"))) ||
		st.Setting("whisper_backend", "") != backend || st.Setting("whisper_release", "") != whisperRelease
	if !need {
		logf("whisper.cpp %s (%s) already installed", whisperRelease, backend)
		return nil
	}
	if err := installWhisper(ctx, backend); err != nil {
		return err
	}
	st.SetSetting("whisper_backend", backend)
	st.SetSetting("whisper_release", whisperRelease)
	return nil
}

// Vulkan build for Windows, cross-compiled for QS-PodScript and shipped in the
// package (whisper.cpp publishes no Windows Vulkan build).
func bundledVulkanPath() string {
	return filepath.Join(P.App, "gpu", "vulkan", exeName("whisper-cli"))
}

func bundledVulkanWhisper() bool { return fileExists(bundledVulkanPath()) }

func installBundledVulkan() error {
	os.RemoveAll(whisperDir())
	if err := os.MkdirAll(whisperDir(), 0o755); err != nil {
		return err
	}
	src, err := os.Open(bundledVulkanPath())
	if err != nil {
		return err
	}
	defer src.Close()
	if err := writeFileFrom(filepath.Join(whisperDir(), exeName("whisper-cli")), src, 0o755); err != nil {
		return err
	}
	logf("whisper.cpp %s (Vulkan) installed", whisperRelease)
	return nil
}

// testWhisper transcribes 11 s of real speech and reports the device whisper
// used ("CUDA - ...", "Vulkan - ...", "CPU") and whether the text came out
// right (catches GPU setups that produce garbage like "!!!!").
func testWhisper(ctx context.Context, st *Store) (string, float64, error) {
	dev, secs, textOK, err := testWhisperArgs(ctx, st, whisperExtraArgs(st))
	if err == nil && !textOK {
		err = fmt.Errorf("whisper ran on %s but produced garbage instead of text", dev)
	}
	return dev, secs, err
}

func testWhisperArgs(ctx context.Context, st *Store, extra []string) (dev string, secs float64, textOK bool, err error) {
	_, mp := whisperModelPath(st)
	dir, err := os.MkdirTemp(P.Work, "check-")
	if err != nil {
		return "", 0, false, err
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "speech.wav")
	if err := os.WriteFile(wav, speechTestWav, 0o644); err != nil {
		return "", 0, false, err
	}
	t0 := time.Now()
	args := append([]string{"-m", relPath(dir, mp), "-f", relPath(dir, wav), "-l", "en", "-nt"}, extra...)
	cmd := whisperCmd(ctx, dir, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err = cmd.Run()
	debugf("whisper test (%s) output:\n%s\n%s", strings.Join(extra, " "), errb.String(), out.String())
	if err != nil {
		return "", 0, false, fmt.Errorf("whisper-cli failed: %v", err)
	}
	text := strings.ToLower(out.String())
	return whisperDeviceSummary(errb.String()), time.Since(t0).Seconds(), strings.Contains(text, speechTestExpect), nil
}

// verifyWhisperOutput makes sure the installed whisper produces real text.
// If it doesn't, flash attention is switched off and it's tested again.
// Returns the device summary; ok=false if it still produces garbage.
func verifyWhisperOutput(ctx context.Context, st *Store) (dev string, ok bool, err error) {
	st.SetSetting("whisper_flash_attn", "1")
	dev, _, ok, err = testWhisperArgs(ctx, st, withFlashAttn(whisperExtraArgs(st), true))
	if err != nil || ok {
		return dev, ok, err
	}
	logf("Whisper produced garbage instead of text on %s - trying again without flash attention...", dev)
	dev, _, ok, err = testWhisperArgs(ctx, st, withFlashAttn(whisperExtraArgs(st), false))
	if err == nil && ok {
		st.SetSetting("whisper_flash_attn", "0")
		logf("Works without flash attention - QS-PodScript will use that on this computer.")
	}
	return dev, ok, err
}

// ensureVADModel downloads the small VAD model when a podcast uses VAD.
func ensureVADModel(ctx context.Context, f Feed) error {
	if !f.VAD || fileExists(vadModelPath()) {
		return nil
	}
	logf("Downloading voice activity detection model")
	return downloadFile(ctx, vadModelURL, vadModelPath(), "VAD model")
}
