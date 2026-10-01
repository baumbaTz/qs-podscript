//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// prepareCUDAPath: when the installer put NVIDIA libraries into
// <install>/cuda, restart once with that folder in LD_LIBRARY_PATH (cuDNN and
// ONNX Runtime load parts of themselves later by name, which only works when
// the folder is on the search path from the start).
func prepareCUDAPath() {
	if runtime.GOOS != "linux" || os.Getenv("QSPS_CUDA_ENV") != "" {
		return
	}
	dir := filepath.Join(exeDir(), "cuda")
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	lp := dir
	if old := os.Getenv("LD_LIBRARY_PATH"); old != "" {
		lp += string(os.PathListSeparator) + old
	}
	env := append(os.Environ(), "LD_LIBRARY_PATH="+lp, "QSPS_CUDA_ENV=1")
	_ = syscall.Exec(exe, os.Args, env) // only returns on error: carry on without
}
