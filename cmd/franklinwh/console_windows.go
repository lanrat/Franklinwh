//go:build windows

package main

import (
	"os"
	"syscall"
)

// attachConsole attaches the process to its parent's console, if any.
//
// The Windows binary is linked for the GUI subsystem (-H=windowsgui) so that
// double-clicking it opens the dashboard without flashing a console window.
// That also means a run from an existing terminal has no console of its own,
// so CLI output would go nowhere. Attaching to the parent console and
// re-pointing the standard handles at it restores normal CLI output. When
// there is no parent console (the double-click case) AttachConsole fails and
// this is a no-op.
//
// Note: because the binary is a GUI-subsystem app, cmd.exe/PowerShell do not
// wait for it, so when used as a CLI the shell prompt returns before output
// appears. Use "start /wait franklinwh ..." to block, or just run the
// dashboard.
func attachConsole() {
	const attachParentProcess = ^uint32(0) // (DWORD)-1 = ATTACH_PARENT_PROCESS
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("AttachConsole")
	if r, _, _ := proc.Call(uintptr(attachParentProcess)); r == 0 {
		return // no parent console: launched by double-click
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = out
		os.Stderr = out
	}
	if in, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		os.Stdin = in
	}
}
