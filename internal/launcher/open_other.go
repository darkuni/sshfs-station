//go:build !windows

package launcher

import (
	"os/exec"
	"runtime"
)

// Open shows url in the default browser (development and demo use only; the
// real app is Windows-only).
func Open(url, dataDir string) (<-chan struct{}, error) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, url)
	if err := cmd.Start(); err != nil {
		return nil, ErrNoBrowser
	}
	go cmd.Wait()
	return nil, nil
}
