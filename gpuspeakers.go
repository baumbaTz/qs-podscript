package main

// Installing graphics card support for speaker detection (NVIDIA / CUDA) -
// from Setup -> Speaker detection, "qs-podscript gpu-speakers install" or the
// installers (which call that command).
//
// What it puts where (Linux / Windows):
//   - sherpa-onnx's own GPU build: the speaker detection library compiled with
//     CUDA support plus the matching ONNX Runtime 1.28.2 for CUDA 13. Kept in
//     data/tools/onnxruntime-gpu and copied next to the program, where the
//     normal (processor-only) libraries are. Those are saved in
//     data/tools/onnxruntime-cpu first, so "remove" can put them back.
//   - the NVIDIA libraries (CUDA 13 runtime, cuBLAS, cuRAND, cuDNN 9, NVRTC)
//     from NVIDIA's official Python packages, unpacked into <install>/cuda.
//     Linux skips this when the system has them already.
//   - Windows only: the Microsoft Visual C++ runtime DLLs the GPU build of
//     ONNX Runtime needs (from the msvc-runtime package), next to the program.
//
// Every download is pinned to a version and checked against its SHA-256.
//
// Libraries the running program has loaded are never overwritten in place:
// Linux replaces the file (the running program keeps the old one), Windows
// renames the old file to *.old-… (deleted at the next start). Speaker
// detection itself runs in a child process, which loads the new libraries
// right away - so no restart is needed.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sherpaGPUVersion must be the sherpa-onnx-go version in go.mod (a test checks).
const sherpaGPUVersion = "1.13.8"

type gpuDownload struct {
	URL, SHA256 string
}

type gpuSpeakerPkg struct {
	Name    string      // sherpa-onnx archive name (also written to version.txt)
	Archive gpuDownload // .tar.bz2 with <Name>/lib/...
	Libs    []string    // taken from <Name>/lib and copied next to the program
	Core    []string    // the libraries the normal package has too (saved before replacing)
	Wheels  []gpuDownload
	MSVC    *gpuDownload // Windows: Visual C++ runtime
	MSVCDLL []string
	SizeMB  int // download, roughly
}

const pypi = "https://files.pythonhosted.org/packages/"

func sherpaRelease(name string) string {
	return "https://github.com/k2-fsa/sherpa-onnx/releases/download/v" + sherpaGPUVersion + "/" + name + ".tar.bz2"
}

var gpuSpeakerPkgs = map[string]gpuSpeakerPkg{
	"linux": {
		Name: "sherpa-onnx-v" + sherpaGPUVersion + "-cuda-13.x-cudnn-9.x-onnxruntime1.28.2-linux-x64-gpu",
		Archive: gpuDownload{sherpaRelease("sherpa-onnx-v" + sherpaGPUVersion + "-cuda-13.x-cudnn-9.x-onnxruntime1.28.2-linux-x64-gpu"),
			"7eddaee14a42d143ec995ed68e9ec3623dd6ca9d22076139d4c0879c3121c819"},
		Libs: []string{"libsherpa-onnx-c-api.so", "libonnxruntime.so", "libonnxruntime_providers_cuda.so", "libonnxruntime_providers_shared.so"},
		Core: []string{"libsherpa-onnx-c-api.so", "libonnxruntime.so"},
		Wheels: []gpuDownload{
			{pypi + "98/8a/3431271f6344874b8f1ac03f16b3d679c91493f8da63f716160403e6d0a0/nvidia_cuda_runtime-13.4.92-py3-none-manylinux2014_x86_64.manylinux_2_17_x86_64.whl", "9641f797da20ce1dd8e779b6e96d08cf9ba564cec8e8225458811ee26423f3a5"},
			{pypi + "7a/38/bdd540bf511d2c9b6f9efc71a81c60cb88e295be0b9312b61d19bbed2212/nvidia_cublas-13.8.0.4-py3-none-manylinux_2_27_x86_64.whl", "9f17797dfcc048694461f4e47de17d2e3c25adf172ef723d2db0a07cd8744b89"},
			{pypi + "07/73/3ee8e5b4cb891401e603ffd3a59b35c6afe785fd2de123afe7c7029603dc/nvidia_curand-10.4.4.72-py3-none-manylinux_2_27_x86_64.whl", "25c3457ae7a224fdd484dab90b0fc5dc0e842fab5db3012afa4a5bd2af4eb7e5"},
			{pypi + "ac/1b/ea72dd62f26ce3e0a7870b85148b75a5987507629b712deebeb87f7cce9a/nvidia_cudnn_cu13-9.26.0.51-py3-none-manylinux_2_27_x86_64.whl", "9c976d539786698c71d6bcdbe2053b7485fae062ff7e1215d2f2ff67b1a2149a"},
			{pypi + "56/9c/1342ebb460ce2afd014ec5a002adc99108e3c53a6b70f399cf64dd1f267d/nvidia_cuda_nvrtc-13.4.92-py3-none-manylinux2010_x86_64.manylinux_2_12_x86_64.whl", "5ce8c97b00b232c4f50c8c4b5a3b68cafee08bdb82ea86f2052ff01d03194f4a"},
		},
		SizeMB: 1300,
	},
	"windows": {
		Name: "sherpa-onnx-v" + sherpaGPUVersion + "-cuda-13.x-cudnn-9.x-onnxruntime1.28.2-win-x64-cuda",
		Archive: gpuDownload{sherpaRelease("sherpa-onnx-v" + sherpaGPUVersion + "-cuda-13.x-cudnn-9.x-onnxruntime1.28.2-win-x64-cuda"),
			"1702e4a1ee68a07422469a7ec1d304a54b43a345ad888daf8e0b0257004a3273"},
		Libs: []string{"sherpa-onnx-c-api.dll", "onnxruntime.dll", "onnxruntime_providers_cuda.dll", "onnxruntime_providers_shared.dll"},
		Core: []string{"sherpa-onnx-c-api.dll", "onnxruntime.dll"},
		Wheels: []gpuDownload{
			{pypi + "86/00/d5436004268f049214193659ebc36550b5ef3925c3d13b4cc980e13be6f5/nvidia_cuda_runtime-13.4.92-py3-none-win_amd64.whl", "08dca5e4aba480c2fd5b55075c0fa71b84ef9dcf0521f2d58baa14a803a7311c"},
			{pypi + "a3/df/f1246959833e2c437db8be3e5b477f66b87f8817821ed40de6c7561c9a36/nvidia_cublas-13.8.0.4-py3-none-win_amd64.whl", "8c5494423bb8a46822cb6b0cb95d7fa4be2d7b96a31155dff083839ec8297910"},
			{pypi + "e2/d4/f59ab342b82ff05ff27d674acd470ba625d788bf71338f7da6c89669d6b9/nvidia_curand-10.4.4.72-py3-none-win_amd64.whl", "e0bce83e083ef25976ee74f59e8f067c15149a74538f1be6c2462e286e7c9c68"},
			{pypi + "d6/1f/9c5d7ed254d7040192b4bb4eef669ba3ed66a3c4576c50f7d2385b255c8e/nvidia_cudnn_cu13-9.26.0.51-py3-none-win_amd64.whl", "8e37a83d7dd7c2663afcae38b1c95c5e9e7c9754f313800da95ca81439fa37f9"},
			{pypi + "64/5b/aa91896f64444eff1dd7f189f4db05046c7f0ba08c0f531283c1c1aac581/nvidia_cuda_nvrtc-13.4.92-py3-none-win_amd64.whl", "6af7ac5372920f6a7a560d0699348560fe44cb52c1d722d6afd3119f8af948c4"},
		},
		MSVC: &gpuDownload{pypi + "21/3b/134d04268ab8e35853cd007582076429b45d60d6abb1036d159be9c50342/msvc_runtime-14.44.35112-cp312-cp312-win_amd64.whl",
			"32f9c706009e16ccc319d6947ce3bffe20e5192bee52b18cf48313f9e7bedfbe"},
		MSVCDLL: []string{"msvcp140.dll", "msvcp140_1.dll", "vcruntime140.dll", "vcruntime140_1.dll"},
		SizeMB:  1500,
	},
}

func gpuSpkDir() string  { return filepath.Join(P.Tools, "onnxruntime-gpu") }
func cpuLibDir() string  { return filepath.Join(P.Tools, "onnxruntime-cpu") }
func cudaLibDir() string { return filepath.Join(exeDir(), "cuda") }

// gpuAtStart: graphics card support was in place when this process started,
// i.e. the libraries it has loaded can use it. Speaker detection runs in a
// child process and isn't bound by this; voiceprints computed in the program
// itself are (until the next start).
var gpuAtStart = gpuLibsInstalled()

// inProcessProvider: the provider for sherpa-onnx calls in this process.
func inProcessProvider() string {
	if forcedProvider == "" && !gpuAtStart {
		return "cpu"
	}
	return providerNow()
}

// ------------------------------------------------------------ can this computer do it?

type gpuSpkSupport struct {
	Possible bool   // NVIDIA card with a new enough driver on Linux / Windows x64
	Card     string // the NVIDIA card, or the other graphics cards found
	Why      string // why not (when !Possible)
	SizeMB   int
}

var (
	supportOnce sync.Once
	supportRes  gpuSpkSupport
)

// gpuSpeakerSupport is checked once per run (nvidia-smi / the adapter list).
func gpuSpeakerSupport() gpuSpkSupport {
	supportOnce.Do(func() { supportRes = checkGPUSpeakerSupport(context.Background()) })
	return supportRes
}

func checkGPUSpeakerSupport(ctx context.Context) gpuSpkSupport {
	pkg, ok := gpuSpeakerPkgs[runtime.GOOS]
	if !ok || runtime.GOARCH != "amd64" {
		return gpuSpkSupport{Why: "graphics card support for speaker detection exists for NVIDIA cards on Windows and Linux only – here it runs on the processor (transcription uses the graphics card all the same)"}
	}
	s := gpuSpkSupport{SizeMB: pkg.SizeMB}
	nv, found := detectNvidia(ctx)
	if !found {
		names := otherGPUNames(ctx)
		hasNV := false
		for _, n := range names {
			if strings.Contains(strings.ToLower(n), "nvidia") {
				hasNV = true
			}
		}
		switch {
		case hasNV:
			s.Card = strings.Join(names, ", ")
			s.Why = "an NVIDIA card is there, but no NVIDIA driver was found (nvidia-smi) – install the NVIDIA driver (580 or newer) first"
		case len(names) > 0:
			s.Card = strings.Join(names, ", ")
			s.Why = "speaker detection can only use NVIDIA graphics cards (CUDA). There is no ready-made version for AMD or Intel cards, so it runs on the processor – transcription still uses your card (Vulkan)"
		default:
			s.Why = "no NVIDIA graphics card found – speaker detection on the graphics card needs one (CUDA)"
		}
		return s
	}
	s.Card = nv.Name
	if cc, ok := nvidiaComputeCap(ctx, nv.Name); ok && cc < minComputeCap {
		s.Why = fmt.Sprintf("the %s is too old for it – CUDA 13 needs a GeForce RTX 20 / GTX 16 series card or newer (compute capability 7.5; this one has %.1f). Transcription can still use it through Vulkan", nv.Name, cc)
		return s
	}
	if cv := nvidiaCUDAVersion(ctx, nv.Driver); cv < 13 {
		s.Why = fmt.Sprintf("the NVIDIA driver %s is too old – it needs driver 580 or newer (CUDA 13). Update the driver and restart QS-PodScript", nv.Driver)
		return s
	}
	s.Possible = true
	return s
}

// minComputeCap: CUDA 13 (and cuDNN for it) dropped Maxwell, Pascal and Volta.
const minComputeCap = 7.5

// nvidiaComputeCap: the card generation ("compute capability", e.g. 8.6 for
// an RTX 3070, 6.1 for a GTX 10 series). Older drivers can't report it - then
// the name decides; ok=false when even that is unclear.
func nvidiaComputeCap(ctx context.Context, name string) (float64, bool) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, nvidiaSMI(), "--query-gpu=compute_cap", "--format=csv,noheader").Output(); err == nil {
		line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		if cc, err := strconv.ParseFloat(line, 64); err == nil {
			return cc, true
		}
	}
	return computeCapFromName(name)
}

func computeCapFromName(name string) (float64, bool) {
	n := strings.ToUpper(name)
	switch {
	case strings.Contains(n, "RTX"), strings.Contains(n, "GTX 16"):
		return 7.5, true
	case regexp.MustCompile(`G?TX? 10[1-8]0|GT 10[1-3]0|TITAN XP?\b|QUADRO P|TESLA P`).MatchString(n):
		return 6.1, true
	case regexp.MustCompile(`GTX (7|9)\d\d|TITAN V|QUADRO GV|TESLA V`).MatchString(n):
		if strings.Contains(n, "V") && !strings.Contains(n, "GTX") {
			return 7.0, true
		}
		return 5.2, true
	}
	return 0, false
}

// nvidiaCUDAVersion: the newest CUDA major version the driver supports.
func nvidiaCUDAVersion(ctx context.Context, driver string) int {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, nvidiaSMI()).Output(); err == nil {
		// "CUDA Version: 13.0" - newer drivers: "CUDA UMD Version: 13.4"
		if m := regexp.MustCompile(`CUDA (?:UMD )?Version: (\d+)\.\d+`).FindSubmatch(out); m != nil {
			n, _ := strconv.Atoi(string(m[1]))
			return n
		}
	}
	major, _ := strconv.Atoi(strings.SplitN(driver, ".", 2)[0])
	switch {
	case major >= 580:
		return 13
	case major >= 525:
		return 12
	case major > 0:
		return 11
	}
	return 0
}

func nvidiaSMI() string {
	if runtime.GOOS == "windows" {
		if p := filepath.Join(os.Getenv("SystemRoot"), "System32", "nvidia-smi.exe"); fileExists(p) {
			return p
		}
	}
	return "nvidia-smi"
}

// otherGPUNames: graphics adapters without asking the NVIDIA driver
// (Windows: WMI, Linux: lspci).
func otherGPUNames(ctx context.Context) []string {
	if runtime.GOOS == "windows" {
		return gpuNames(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lspci").Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		for _, k := range []string{"VGA compatible controller: ", "3D controller: ", "Display controller: "} {
			if i := strings.Index(l, k); i >= 0 {
				names = append(names, strings.TrimSpace(l[i+len(k):]))
			}
		}
	}
	return names
}

// ------------------------------------------------------------ install / remove

// installGPUSpeakers downloads and switches on graphics card support, then
// tests it. The returned info says whether the test passed.
func installGPUSpeakers(ctx context.Context, force bool) (cudaGPUInfo, error) {
	pkg, ok := gpuSpeakerPkgs[runtime.GOOS]
	if !ok || runtime.GOARCH != "amd64" {
		return cudaGPUInfo{}, fmt.Errorf("not available on this system")
	}
	if !force {
		if s := checkGPUSpeakerSupport(ctx); !s.Possible {
			return cudaGPUInfo{}, fmt.Errorf("%s", s.Why)
		}
	}
	tmp := filepath.Join(P.Tools, "gpu-download")
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return cudaGPUInfo{}, err
	}
	defer os.RemoveAll(tmp)
	logf("Graphics card support for speaker detection: installing (%s)", pkg.Name)

	// 1. the speaker detection library with CUDA support
	if v, _ := os.ReadFile(filepath.Join(gpuSpkDir(), "version.txt")); strings.TrimSpace(string(v)) != pkg.Name || !allExist(gpuSpkDir(), pkg.Libs) {
		arch := filepath.Join(tmp, "sherpa-gpu.tar.bz2")
		if err := fetchChecked(ctx, pkg.Archive, arch, "speaker detection for NVIDIA"); err != nil {
			return cudaGPUInfo{}, err
		}
		setStage("Unpacking the speaker detection libraries", -1)
		want := map[string]bool{}
		for _, l := range pkg.Libs {
			want[pkg.Name+"/lib/"+l] = true
		}
		unpack := filepath.Join(tmp, "sherpa")
		if _, err := extractTar(arch, unpack, func(n string) string {
			n = strings.TrimPrefix(n, "./")
			if want[n] {
				return path.Base(n)
			}
			return ""
		}); err != nil {
			return cudaGPUInfo{}, fmt.Errorf("unpacking %s: %w", pkg.Name, err)
		}
		os.Remove(arch)
		if !allExist(unpack, pkg.Libs) {
			return cudaGPUInfo{}, fmt.Errorf("the sherpa-onnx archive looks different than expected (libraries missing)")
		}
		os.WriteFile(filepath.Join(unpack, "version.txt"), []byte(pkg.Name+"\n"), 0o644)
		os.RemoveAll(gpuSpkDir())
		if err := os.Rename(unpack, gpuSpkDir()); err != nil {
			return cudaGPUInfo{}, err
		}
	}

	// 2. NVIDIA's libraries
	if runtime.GOOS == "linux" && systemHasCUDA13() {
		os.RemoveAll(cudaLibDir())
		logf("   using the CUDA 13 and cuDNN 9 libraries installed on this system")
	} else if !fileExists(filepath.Join(cudaLibDir(), ".complete")) {
		staging := cudaLibDir() + ".new"
		os.RemoveAll(staging)
		for i, w := range pkg.Wheels {
			name := path.Base(w.URL)
			label := strings.Join(strings.SplitN(name, "-", 3)[:2], " ")
			setStage(fmt.Sprintf("NVIDIA libraries %d of %d", i+1, len(pkg.Wheels)), -1)
			f := filepath.Join(tmp, name)
			if err := fetchChecked(ctx, w, f, label); err != nil {
				return cudaGPUInfo{}, err
			}
			if _, err := extractZip(f, staging, nvidiaLibFilter); err != nil {
				return cudaGPUInfo{}, fmt.Errorf("unpacking %s: %w", name, err)
			}
			os.Remove(f)
		}
		os.WriteFile(filepath.Join(staging, ".complete"), []byte(time.Now().Format(time.RFC3339)), 0o644)
		removeDirOrLater(cudaLibDir())
		if err := os.Rename(staging, cudaLibDir()); err != nil {
			return cudaGPUInfo{}, fmt.Errorf("could not put the NVIDIA libraries in place: %w", err)
		}
	}

	// 3. Windows: Visual C++ runtime next to the program (the GPU build of
	// ONNX Runtime needs it, the normal one doesn't)
	if pkg.MSVC != nil && !allExist(exeDir(), pkg.MSVCDLL) {
		f := filepath.Join(tmp, "msvc.whl")
		if err := fetchChecked(ctx, *pkg.MSVC, f, "Visual C++ runtime"); err != nil {
			return cudaGPUInfo{}, err
		}
		want := map[string]bool{}
		for _, d := range pkg.MSVCDLL {
			want[d] = true
		}
		dir := filepath.Join(tmp, "msvc")
		if _, err := extractZip(f, dir, func(n string) string {
			if strings.Contains(n, "/Scripts/") && want[strings.ToLower(path.Base(n))] {
				return strings.ToLower(path.Base(n))
			}
			return ""
		}); err != nil {
			return cudaGPUInfo{}, err
		}
		for _, d := range pkg.MSVCDLL {
			if err := replaceLib(filepath.Join(dir, d), filepath.Join(exeDir(), d)); err != nil {
				return cudaGPUInfo{}, err
			}
		}
	}

	// 4. switch: save the processor-only libraries, put the GPU build in place
	if err := applyGPULibs(pkg); err != nil {
		return cudaGPUInfo{}, err
	}

	// 5. does the program still start with them? If not, undo at once.
	setStage("Testing the graphics card", -1)
	if out, err := exec.CommandContext(ctx, selfExe(), "lib-check").CombinedOutput(); err != nil || !strings.Contains(string(out), "LIBS OK") {
		why := lastLines(string(out), 2)
		if why == "" && err != nil {
			why = err.Error()
		}
		removeGPUSpeakers()
		return cudaGPUInfo{}, fmt.Errorf("the graphics card libraries don't load on this computer (%s) – removed again", why)
	}
	os.Remove(gpuCrashMarker())
	g, _ := runGPUCheck()
	gpuMu.Lock()
	gpuRes, gpuDone = g, true
	gpuMu.Unlock()
	if g.OK {
		logf("Graphics card support for speaker detection: installed and working (%s)", g.Name)
	} else {
		logf("Graphics card support for speaker detection: installed, but the test failed: %s", g.Reason)
	}
	return g, nil
}

// removeGPUSpeakers puts the processor-only libraries back and deletes the
// downloaded parts.
func removeGPUSpeakers() error {
	pkg, ok := gpuSpeakerPkgs[runtime.GOOS]
	if !ok {
		return nil
	}
	var errs []string
	for _, l := range pkg.Core {
		if b := filepath.Join(cpuLibDir(), l); fileExists(b) {
			if err := replaceLib(b, filepath.Join(exeDir(), l)); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	// without a saved copy the GPU build stays: it works on the processor too
	for _, l := range pkg.Libs {
		if strings.Contains(l, "providers_") {
			if err := removeLib(filepath.Join(exeDir(), l)); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	os.RemoveAll(gpuSpkDir())
	os.RemoveAll(cpuLibDir())
	removeDirOrLater(cudaLibDir())
	gpuMu.Lock()
	gpuDone, gpuRes = true, cudaGPUInfo{Reason: "graphics card support is not installed"}
	gpuMu.Unlock()
	logf("Graphics card support for speaker detection: removed")
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func applyGPULibs(pkg gpuSpeakerPkg) error {
	if err := os.MkdirAll(cpuLibDir(), 0o755); err != nil {
		return err
	}
	for _, l := range pkg.Core {
		cur := filepath.Join(exeDir(), l)
		if fileExists(cur) && !sameContent(cur, filepath.Join(gpuSpkDir(), l)) {
			if err := copyLibFile(cur, filepath.Join(cpuLibDir(), l)); err != nil {
				return fmt.Errorf("saving %s: %w", l, err)
			}
		}
	}
	for _, l := range pkg.Libs {
		src, dst := filepath.Join(gpuSpkDir(), l), filepath.Join(exeDir(), l)
		if sameContent(src, dst) {
			continue
		}
		if err := replaceLib(src, dst); err != nil {
			return err
		}
	}
	return nil
}

// reapplyGPUSpeakers (at start): an update copied the package's
// processor-only libraries over the GPU build - put it back (no download).
// This process has the old ones loaded already, so it computes voiceprints
// on the processor until the next start; speaker detection (child process)
// uses the graphics card right away.
func reapplyGPUSpeakers() {
	pkg, ok := gpuSpeakerPkgs[runtime.GOOS]
	if !ok {
		return
	}
	if v, _ := os.ReadFile(filepath.Join(gpuSpkDir(), "version.txt")); strings.TrimSpace(string(v)) != pkg.Name || !allExist(gpuSpkDir(), pkg.Libs) {
		return
	}
	if !allExist(exeDir(), pkg.MSVCDLL) { // Windows: without them the program wouldn't start
		return
	}
	applied := true
	for _, l := range pkg.Libs {
		a, b := filepath.Join(gpuSpkDir(), l), filepath.Join(exeDir(), l)
		if strings.Contains(l, "providers_") { // big: the size is enough
			sa, ea := os.Stat(a)
			sb, eb := os.Stat(b)
			if ea != nil || eb != nil || sa.Size() != sb.Size() {
				applied = false
			}
		} else if !sameContent(a, b) {
			applied = false
		}
	}
	if applied {
		return
	}
	if err := applyGPULibs(pkg); err != nil {
		logf("Graphics card support for speaker detection could not be put back after the update: %v", err)
		return
	}
	gpuAtStart = false
	logf("Graphics card support for speaker detection: put back in place after the update")
}

// nvidiaLibFilter: the shared libraries from NVIDIA's wheels, flattened.
func nvidiaLibFilter(n string) string {
	if !strings.HasPrefix(n, "nvidia/") {
		return ""
	}
	b := path.Base(n)
	low := strings.ToLower(b)
	if strings.HasPrefix(low, "libnvblas") || strings.HasPrefix(low, "nvblas") {
		return ""
	}
	if runtime.GOOS == "windows" {
		if strings.HasSuffix(low, ".dll") {
			return b
		}
		return ""
	}
	if strings.Contains(n, "/lib/") && strings.Contains(b, ".so") {
		return b
	}
	return ""
}

var cudaSonames = []string{"libcudart.so.13", "libcublas.so.13", "libcublasLt.so.13", "libcurand.so.10", "libcudnn.so.9", "libnvrtc.so.13"}

func systemHasCUDA13() bool {
	out, err := exec.Command("ldconfig", "-p").Output()
	if err != nil {
		out, err = exec.Command("/sbin/ldconfig", "-p").Output()
		if err != nil {
			return false
		}
	}
	s := string(out)
	for _, so := range cudaSonames {
		if !strings.Contains(s, " "+so+" ") {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------ file helpers

func fetchChecked(ctx context.Context, d gpuDownload, dest, label string) error {
	if err := downloadFile(ctx, d.URL, dest, label); err != nil {
		return err
	}
	sum, err := fileSHA256(dest)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sum, d.SHA256) {
		os.Remove(dest)
		return fmt.Errorf("checksum mismatch for %s – download it again later", path.Base(d.URL))
	}
	return nil
}

func allExist(dir string, names []string) bool {
	for _, n := range names {
		if !fileExists(filepath.Join(dir, n)) {
			return false
		}
	}
	return true
}

func sameContent(a, b string) bool {
	sa, ea := os.Stat(a)
	sb, eb := os.Stat(b)
	if ea != nil || eb != nil || sa.Size() != sb.Size() {
		return false
	}
	ha, ea := fileSHA256(a)
	hb, eb := fileSHA256(b)
	return ea == nil && eb == nil && ha == hb
}

func copyLibFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, st.Mode().Perm()|0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// replaceLib puts src at dst without touching a library a running program
// has loaded: the old file is replaced (Linux) or renamed away (Windows).
func replaceLib(src, dst string) error {
	tmp := dst + ".new"
	if err := copyLibFile(src, tmp); err != nil {
		return fmt.Errorf("copying %s: %w", filepath.Base(dst), err)
	}
	if runtime.GOOS == "windows" && fileExists(dst) {
		if err := os.Rename(dst, oldName(dst)); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("replacing %s: %w", filepath.Base(dst), err)
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("replacing %s: %w", filepath.Base(dst), err)
	}
	return nil
}

func removeLib(p string) error {
	if !fileExists(p) {
		return nil
	}
	if err := os.Remove(p); err == nil {
		return nil
	}
	return os.Rename(p, oldName(p)) // Windows: in use - deleted at the next start
}

func oldName(p string) string { return p + ".old-" + strconv.FormatInt(time.Now().UnixNano(), 36) }

// removeDirOrLater: a folder with DLLs in use (Windows) can't be deleted -
// then it goes at the next start.
func removeDirOrLater(dir string) {
	if _, err := os.Stat(dir); err != nil {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		os.Rename(dir, oldName(dir))
		os.WriteFile(filepath.Join(P.Data, "remove-at-start"), []byte(dir+"\n"), 0o644)
	}
}

// cleanupOldLibs (at start): leftovers of replaced libraries.
func cleanupOldLibs() {
	matches, _ := filepath.Glob(filepath.Join(exeDir(), "*.old-*"))
	for _, m := range matches {
		os.RemoveAll(m)
	}
	if b, err := os.ReadFile(filepath.Join(P.Data, "remove-at-start")); err == nil {
		for _, d := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if d != "" && strings.HasPrefix(filepath.Clean(d), exeDir()) && !fileExists(filepath.Join(d, ".complete")) {
				os.RemoveAll(d)
			}
		}
		os.Remove(filepath.Join(P.Data, "remove-at-start"))
	}
}

func selfExe() string {
	exe, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	return exe
}

// ------------------------------------------------------------ command line

// cmdGPUSpeakers: qs-podscript gpu-speakers [install|remove|status] [--force]
// The last line starts with GPU-SPEAKERS (for the installers). Exit code:
// 0 = done / working, 1 = failed, 2 = not possible on this computer.
func cmdGPUSpeakers(ctx context.Context, args []string) error {
	what, force := "status", false
	for _, a := range args {
		switch a {
		case "--force", "-force":
			force = true
		case "install", "on", "remove", "off", "status":
			what = a
		default:
			return fmt.Errorf("usage: qs-podscript gpu-speakers [install|remove|status] [--force]")
		}
	}
	unsupported := func(why string) error {
		fmt.Printf("GPU-SPEAKERS UNSUPPORTED %s\n", why)
		os.Exit(2)
		return nil
	}
	failed := func(format string, a ...any) error {
		fmt.Printf("GPU-SPEAKERS FAILED "+format+"\n", a...)
		os.Exit(1)
		return nil
	}
	switch what {
	case "status":
		s := checkGPUSpeakerSupport(ctx)
		switch {
		case gpuLibsInstalled():
			g, _ := runGPUCheck()
			if g.OK {
				fmt.Printf("GPU-SPEAKERS OK installed, speaker detection uses %s\n", g.Name)
				return nil
			}
			return failed("installed, but the graphics card can't be used: %s", g.Reason)
		case s.Possible:
			fmt.Printf("GPU-SPEAKERS POSSIBLE not installed; %s could do it (about %d MB download): qs-podscript gpu-speakers install\n", s.Card, s.SizeMB)
			return nil
		default:
			return unsupported(s.Why)
		}
	case "remove", "off":
		if err := removeGPUSpeakers(); err != nil {
			return failed("%v", err)
		}
		fmt.Println("GPU-SPEAKERS REMOVED speaker detection runs on the processor")
		return nil
	}
	if !force {
		if s := checkGPUSpeakerSupport(ctx); !s.Possible {
			return unsupported(s.Why)
		}
	}
	g, err := installGPUSpeakers(ctx, force)
	switch {
	case err != nil:
		return failed("%v", err)
	case g.OK:
		fmt.Printf("GPU-SPEAKERS OK speaker detection uses %s (%s)\n", g.Name, gpuTimes(g.Detail))
	case strings.Contains(g.Reason, "not downloaded yet"):
		fmt.Println("GPU-SPEAKERS INSTALLED the graphics card is tested once the voice models are downloaded (Setup)")
	default:
		return failed("installed, but the graphics card can't be used: %s", g.Reason)
	}
	return nil
}

func gpuTimes(detail string) string {
	if m := regexp.MustCompile(`cpu=\d+ms gpu=\d+ms`).FindString(detail); m != "" {
		return m
	}
	return "tested"
}

// cmdLibCheck (internal): this program starts with the libraries next to it.
func cmdLibCheck() error {
	fmt.Println("LIBS OK")
	return nil
}
