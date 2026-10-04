package main

// Speaker detection on the graphics card (NVIDIA / CUDA).
//
// The normal package ships the CPU build of ONNX Runtime. The installer can
// replace it with the GPU build of the same ONNX Runtime version (it still
// runs everything on the CPU when asked to) and put the NVIDIA libraries it
// needs (CUDA runtime, cuBLAS, cuRAND, cuDNN, NVRTC) into <install>/cuda.
//
// Whether that really works on this computer is tested once per start in a
// child process ("qs-podscript gpu-check"): if CUDA can't be loaded, a crash
// or a silent fallback there doesn't affect the app, which then simply
// stays on the CPU.

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

const (
	deviceAuto = "auto" // graphics card when it works, else CPU
	deviceCPU  = "cpu"
)

type cudaGPUInfo struct {
	Installed bool   // GPU build of ONNX Runtime present
	OK        bool   // check passed: speaker detection runs on the graphics card
	Name      string // e.g. "NVIDIA GeForce RTX 3070"
	Reason    string // why not (when !OK)
	Detail    string // check output (log)
}

var (
	gpuMu      sync.Mutex
	gpuDone    bool
	gpuRes     cudaGPUInfo
	devMu      sync.RWMutex
	devSetting = deviceAuto
)

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return filepath.Dir(exe)
}

func cudaProviderLib() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(exeDir(), "onnxruntime_providers_cuda.dll")
	}
	return filepath.Join(exeDir(), "libonnxruntime_providers_cuda.so")
}

func gpuLibsInstalled() bool { return fileExists(cudaProviderLib()) }

// setSpeakerDevice is called at start and when the setting changes.
func setSpeakerDevice(v string) {
	if v != deviceCPU {
		v = deviceAuto
	}
	devMu.Lock()
	devSetting = v
	devMu.Unlock()
}

func speakerDevice() string {
	devMu.RLock()
	defer devMu.RUnlock()
	return devSetting
}

// gpuStatus runs the check once (blocking the first caller).
func gpuStatus() cudaGPUInfo {
	gpuMu.Lock()
	defer gpuMu.Unlock()
	if !gpuDone {
		var final bool
		gpuRes, final = runGPUCheck()
		gpuDone = final
		if gpuRes.Installed {
			if gpuRes.OK {
				logf("Speaker detection runs on the graphics card (%s)", gpuRes.Name)
			} else {
				logf("Speaker detection stays on the CPU: %s", gpuRes.Reason)
				if gpuRes.Detail != "" {
					logf("   graphics card check said: %s", gpuRes.Detail)
				}
			}
		}
	}
	return gpuRes
}

// startGPUCheck runs the check in the background so it is ready when the
// first episode gets there.
func startGPUCheck() {
	if gpuLibsInstalled() {
		go gpuStatus()
	}
}

// speakerProvider: the ONNX Runtime provider for speaker detection and voiceprints.
func speakerProvider(_ *Store) string { return providerNow() }

func providerNow() string {
	if forcedProvider != "" { // child process: decided by the parent
		return forcedProvider
	}
	if speakerDevice() == deviceCPU || !gpuLibsInstalled() {
		return "cpu"
	}
	if gpuStatus().OK {
		return "cuda"
	}
	return "cpu"
}

func providerText(p string) string {
	if p == "cuda" {
		if n := gpuRes.Name; n != "" {
			return "graphics card (" + n + ")"
		}
		return "graphics card (CUDA)"
	}
	return "CPU"
}

// markGPUBroken: the graphics card failed during real work - use the CPU
// for the rest of this run.
func markGPUBroken(why string) {
	gpuMu.Lock()
	gpuDone, gpuRes.OK = true, false
	gpuRes.Reason = "it failed during speaker detection: " + why
	gpuMu.Unlock()
}

// A crash inside the graphics card code can't be caught (the process ends).
// The marker exists while speaker detection runs on the graphics card; if it
// is still there at the next start, the graphics card is not used again until
// the user saves the speaker settings.
func gpuCrashMarker() string { return filepath.Join(P.Data, "gpu-speaker-detection-running") }

var (
	guardMu sync.Mutex
	guardN  int
)

func gpuGuard(provider string) func() {
	if provider == "cpu" {
		return func() {}
	}
	guardMu.Lock()
	if guardN == 0 {
		os.WriteFile(gpuCrashMarker(), []byte(time.Now().Format(time.RFC3339)), 0o644)
	}
	guardN++
	guardMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			guardMu.Lock()
			if guardN--; guardN == 0 {
				os.Remove(gpuCrashMarker())
			}
			guardMu.Unlock()
		})
	}
}

// resetGPUCheck: after the user saved the speaker settings - forget a crash
// and test again.
func resetGPUCheck() {
	os.Remove(gpuCrashMarker())
	gpuMu.Lock()
	gpuDone = false
	gpuMu.Unlock()
	startGPUCheck()
}

// gpuDoneOK: the check ran and passed (without waiting for it).
func gpuDoneOK() bool {
	if !gpuMu.TryLock() {
		return false
	}
	defer gpuMu.Unlock()
	return gpuDone && gpuRes.OK
}

// deviceSummary: one line for the Setup page / manual.
func deviceSummary() string {
	if !gpuLibsInstalled() {
		return "CPU"
	}
	if !gpuMu.TryLock() {
		return "checking the graphics card …"
	}
	gpuMu.Unlock()
	g := gpuStatus()
	switch {
	case speakerDevice() == deviceCPU && g.OK:
		return "CPU (set below; the graphics card " + g.Name + " would work)"
	case speakerDevice() == deviceCPU:
		return "CPU (set below)"
	case g.OK:
		return "graphics card – " + g.Name
	default:
		return "CPU – the graphics card can't be used: " + g.Reason
	}
}

// runGPUCheck starts "qs-podscript gpu-check" and reads its verdict.
// final=false: try again later (e.g. models not downloaded yet).
func runGPUCheck() (cudaGPUInfo, bool) {
	g := cudaGPUInfo{Installed: gpuLibsInstalled()}
	if !g.Installed {
		g.Reason = "graphics card support is not installed"
		return g, true
	}
	if fileExists(gpuCrashMarker()) {
		g.Reason = "QS-PodScript stopped unexpectedly during speaker detection on the graphics card last time, so it now uses the CPU. Save the speaker settings to try the graphics card again."
		return g, true
	}
	if !fileExists(embeddingModelPath()) {
		g.Reason = "voiceprint model not downloaded yet"
		return g, false
	}
	exe, err := os.Executable()
	if err != nil {
		g.Reason = err.Error()
		return g, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "gpu-check").CombinedOutput()
	text := string(out)
	g.Detail = errorLines(text)
	switch {
	case strings.Contains(text, "GPU-CHECK NO-GPU") || strings.Contains(text, "Fallback to cpu"):
		g.Reason = "the speaker detection library next to the program has no graphics card support (an update put the normal one back?) – install graphics card support again (Setup → Speaker detection)"
	case strings.Contains(text, "GPU-CHECK OK"):
		g.OK = true
		g.Name = gpuName()
	case strings.Contains(text, "GPU-CHECK DIFFERENT"):
		g.Reason = "results on the graphics card differ from the CPU"
	case strings.Contains(text, "libcuda.so") || strings.Contains(text, "nvcuda.dll") || strings.Contains(text, "driver version is insufficient"):
		g.Reason = "the NVIDIA driver is missing or too old (CUDA 13 needs driver 580 or newer)"
	case strings.Contains(text, "no kernel image") || strings.Contains(text, "ARCH_MISMATCH") || strings.Contains(text, "not supported on this GPU"):
		g.Reason = "this graphics card is too old for CUDA 13 (needs a GeForce RTX 20 / GTX 16 series card or newer)"
	case strings.Contains(text, "cudnn"):
		g.Reason = "cuDNN (NVIDIA library) is missing or doesn't fit – run the installer again"
	case strings.Contains(text, "libcu") || strings.Contains(text, "libnvrtc") || strings.Contains(text, "cublas") || strings.Contains(text, "cudart"):
		g.Reason = "NVIDIA CUDA libraries are missing or don't fit – run the installer again"
	case strings.Contains(text, "out of memory"):
		g.Reason = "not enough memory on the graphics card"
	case ctx.Err() != nil:
		g.Reason = "the check took too long"
	case err != nil:
		g.Reason = "the check failed (" + err.Error() + ")"
	default:
		g.Reason = "the check failed"
	}
	return g, true
}

// errorLines: the lines that explain a failed check (not the stack dump).
func errorLines(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if strings.Contains(l, "GPU-CHECK") || strings.Contains(l, "Failed") || strings.Contains(l, "what():") ||
			strings.Contains(l, "Fallback") || strings.Contains(l, "rror") {
			if i := strings.Index(l, "] "); i > 0 && i < 120 && strings.HasPrefix(l, "\x1b") {
				l = l[i+2:]
			}
			keep = append(keep, l)
		}
		if len(keep) == 3 {
			break
		}
	}
	if len(keep) == 0 {
		return lastLines(s, 2)
	}
	return strings.Join(keep, " | ")
}

func lastLines(s string, n int) string {
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			keep = append(keep, l)
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, " | ")
}

func gpuName() string {
	out, err := exec.Command("nvidia-smi", "--query-gpu=name", "--format=csv,noheader").Output()
	if err != nil {
		return "NVIDIA"
	}
	return strings.TrimSpace(strings.Split(string(out), "\n")[0])
}

// cmdGPUCheck (child process): compute the same voiceprint on the CPU and the
// graphics card and compare.
// cmdGPUCheck: "qs-podscript gpu-check" runs the real test in a second
// process ("gpu-check --inner") and reads everything it prints: a failing
// CUDA start can crash it, and sherpa-onnx built without GPU support only
// prints a warning and quietly uses the processor - neither may count as OK.
func cmdGPUCheck(args []string) error {
	if len(args) > 0 && args[0] == "--inner" {
		return gpuCheckInner()
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	out, runErr := exec.Command(exe, "gpu-check", "--inner").CombinedOutput()
	text := string(out)
	if strings.Contains(text, "Fallback to cpu") {
		text = strings.ReplaceAll(text, "GPU-CHECK OK", "(would be OK, but ran on the processor)")
		text += "GPU-CHECK NO-GPU the speaker detection library has no graphics card support (it fell back to the processor)\n"
	}
	fmt.Print(text)
	if runErr != nil && !strings.Contains(text, "GPU-CHECK") {
		return fmt.Errorf("the graphics card test stopped: %v", runErr)
	}
	return nil
}

func gpuCheckInner() error {
	samples := checkSignal(4 * sampleRate)
	embedWith := func(provider string) ([]float32, time.Duration, error) {
		ex := sherpa.NewSpeakerEmbeddingExtractor(&sherpa.SpeakerEmbeddingExtractorConfig{
			Model: embeddingModelPath(), NumThreads: 1, Provider: provider,
		})
		if ex == nil {
			return nil, 0, fmt.Errorf("could not load the voiceprint model (%s)", provider)
		}
		defer sherpa.DeleteSpeakerEmbeddingExtractor(ex)
		var v []float32
		var el time.Duration
		for i := 0; i < 2; i++ { // second run = warm
			t0 := time.Now()
			st := ex.CreateStream()
			st.AcceptWaveform(sampleRate, samples)
			st.InputFinished()
			v = ex.Compute(st)
			sherpa.DeleteOnlineStream(st)
			el = time.Since(t0)
		}
		return v, el, nil
	}
	cpu, tc, err := embedWith("cpu")
	if err != nil {
		return err
	}
	gpu, tg, err := embedWith("cuda")
	if err != nil {
		return err
	}
	var ab, aa, bb float64
	for i := range cpu {
		ab += float64(cpu[i] * gpu[i])
		aa += float64(cpu[i] * cpu[i])
		bb += float64(gpu[i] * gpu[i])
	}
	cos := ab / math.Sqrt(aa*bb)
	if cos < 0.99 {
		fmt.Printf("GPU-CHECK DIFFERENT cos=%.4f\n", cos)
		return nil
	}
	fmt.Printf("GPU-CHECK OK cos=%.5f cpu=%dms gpu=%dms\n", cos, tc.Milliseconds(), tg.Milliseconds())
	return nil
}

// checkSignal: a deterministic voice-like test signal (harmonics with a
// wandering pitch plus a little noise).
func checkSignal(n int) []float32 {
	s := make([]float32, n)
	var ph float64
	seed := uint32(12345)
	for i := range s {
		t := float64(i) / sampleRate
		f0 := 140 + 30*math.Sin(2*math.Pi*0.7*t)
		ph += 2 * math.Pi * f0 / sampleRate
		v := 0.0
		for h := 1; h <= 8; h++ {
			v += math.Sin(float64(h)*ph) / float64(h)
		}
		seed = seed*1664525 + 1013904223
		noise := float64(seed>>8)/float64(1<<24) - 0.5
		env := 0.5 + 0.5*math.Sin(2*math.Pi*3*t)
		s[i] = float32(0.25*v*env + 0.02*noise)
	}
	return s
}
