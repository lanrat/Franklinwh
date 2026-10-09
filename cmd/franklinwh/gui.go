package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/lanrat/franklinwh/internal/gui"
)

// runGUI starts the local web UI and blocks until the user quits (via the
// page, Ctrl+C, or the browser going away).
func runGUI(cfg gui.Config, openBrowserOnStart bool) error {
	s, err := gui.New(cfg)
	if err != nil {
		return err
	}
	url, err := s.Start()
	if err != nil {
		return err
	}
	defer s.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(os.Stderr, "FranklinWH dashboard running at %s\n", url)
	if openBrowserOnStart {
		go s.WatchHeartbeat()
		if err := openBrowser(url); err != nil {
			fmt.Fprintf(os.Stderr, "could not open a browser automatically; open %s yourself\n", url)
		}
	} else {
		fmt.Fprintf(os.Stderr, "open %s in your browser\n", url)
	}

	select {
	case <-ctx.Done():
	case <-s.Done():
	}
	return s.Close()
}

// openBrowser opens url in the user's default browser.
func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	case "darwin":
		cmd, args = "open", []string{url}
	default:
		cmd, args = "xdg-open", []string{url}
	}
	return exec.Command(cmd, args...).Start()
}
