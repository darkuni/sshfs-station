//go:build windows

package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func appBrowsers() []string {
	var out []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LOCALAPPDATA"} {
		root := os.Getenv(env)
		if root == "" {
			continue
		}
		out = append(out,
			filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"))
	}
	return out
}

// Open shows url in its own app-style window (Edge, which ships with Windows,
// or Chrome), using a private profile under dataDir so it never mixes with the
// person's normal browsing. Falls back to the default browser. The returned
// channel closes when the window's browser process exits; it is nil when the
// window cannot be tracked (default browser fallback).
func Open(url, dataDir string) (<-chan struct{}, error) {
	profile := filepath.Join(dataDir, "window")
	_ = DisableBrowserExtras(profile) // best effort: the window still works without it
	for _, exe := range appBrowsers() {
		if _, err := os.Stat(exe); err != nil {
			continue
		}
		cmd := exec.Command(exe,
			"--app="+url,
			"--user-data-dir="+profile,
			"--window-size=1280,920",
			"--no-first-run", "--no-default-browser-check",
			"--disable-features=Translate,msEdgeSidebarV2,AutofillServerCommunication",
		)
		if err := cmd.Start(); err == nil {
			done := make(chan struct{})
			go func() { cmd.Wait(); close(done) }()
			return done, nil
		}
	}
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return nil, ErrNoBrowser
	}
	go cmd.Wait()
	return nil, nil
}
