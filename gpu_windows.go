//go:build windows

package main

import (
	"os"
	"path/filepath"
)

// prepareCUDAPath: NVIDIA DLLs in <install>\cuda are found via PATH
// (Windows searches PATH when ONNX Runtime loads its CUDA part later).
func prepareCUDAPath() {
	dir := filepath.Join(exeDir(), "cuda")
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
}
