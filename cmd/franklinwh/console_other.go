//go:build !windows

package main

// attachConsole is a no-op on non-Windows platforms, which do not use the
// GUI subsystem and keep their console as usual.
func attachConsole() {}
