//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	user32            = syscall.NewLazyDLL("user32.dll")
	procAttachConsole = kernel32.NewProc("AttachConsole")
	procMessageBox    = user32.NewProc("MessageBoxW")
)

const attachParentProcess = ^uintptr(0) // (DWORD)-1

func validHandle(h syscall.Handle) bool { return h != 0 && h != syscall.InvalidHandle }

// setupConsole makes printing work for the windowed build (which has no console
// of its own). It returns false when there is nowhere to print, for example
// when started from a desktop shortcut; message boxes are used then.
func setupConsole() bool {
	out, _ := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	if validHandle(out) {
		return true // console build, or output redirected to a file/pipe
	}
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		return false
	}
	f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	os.Stdout, os.Stderr = f, f
	f.WriteString("\r\n")
	return true
}

func messageBox(title, msg string, isError bool) {
	flags := uintptr(0x40) // MB_ICONINFORMATION
	if isError {
		flags = 0x10 // MB_ICONERROR
	}
	flags |= 0x10000 | 0x40000 // MB_SETFOREGROUND | MB_TOPMOST
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(msg)
	procMessageBox.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), flags)
}
