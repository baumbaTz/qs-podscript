//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// pauseIfOwnConsole keeps the window open when the exe was double-clicked
// (then this process is the only one attached to its console), so an error
// message can be read before the window disappears.
func pauseIfOwnConsole() {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleProcessList")
	var buf [4]uint32
	n, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 1 {
		fmt.Print("\nPress Enter to close...")
		fmt.Scanln()
	}
}
