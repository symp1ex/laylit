package winconsole

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

const attachParentProcess = ^uint32(0)

var (
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole    = kernel32.NewProc("AttachConsole")
	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
)

// Outputs attaches a windowsgui build to its parent console for explicit CLI
// commands. The no-argument background path never calls AttachConsole.
func Outputs(attach bool) (io.Writer, io.Writer, func()) {
	if !attach {
		return os.Stdout, os.Stderr, func() {}
	}
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd != 0 {
		return os.Stdout, os.Stderr, func() {}
	}
	ok, _, _ := procAttachConsole.Call(uintptr(attachParentProcess))
	if ok == 0 {
		return os.Stdout, os.Stderr, func() {}
	}
	name, _ := windows.UTF16PtrFromString("CONOUT$")
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE|windows.GENERIC_READ, windows.FILE_SHARE_WRITE|windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return os.Stdout, os.Stderr, func() {}
	}
	file := os.NewFile(uintptr(handle), "CONOUT$")
	return file, file, func() { _ = file.Close() }
}
