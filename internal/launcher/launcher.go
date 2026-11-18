// Package launcher opens the app window and keeps one copy of the app running.
package launcher

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type instance struct {
	URL string `json:"url"`
	PID int    `json:"pid"`
}

func instancePath(dataDir string) string { return filepath.Join(dataDir, "instance.json") }

// Running returns the URL of an already running copy of the app, if any.
func Running(dataDir string) (string, bool) {
	raw, err := os.ReadFile(instancePath(dataDir))
	if err != nil {
		return "", false
	}
	var in instance
	if json.Unmarshal(raw, &in) != nil || in.URL == "" {
		return "", false
	}
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(in.URL)
	if err != nil {
		return "", false
	}
	resp.Body.Close()
	return in.URL, resp.StatusCode == http.StatusOK
}

// Register records this copy of the app so a second launch can find it.
func Register(dataDir, url string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	raw, _ := json.Marshal(instance{URL: url, PID: os.Getpid()})
	return os.WriteFile(instancePath(dataDir), raw, 0o600)
}

// Unregister removes the record if it is still ours.
func Unregister(dataDir string) {
	raw, err := os.ReadFile(instancePath(dataDir))
	if err != nil {
		return
	}
	var in instance
	if json.Unmarshal(raw, &in) == nil && in.PID == os.Getpid() {
		os.Remove(instancePath(dataDir))
	}
}

// ErrNoBrowser means no way was found to show the window.
var ErrNoBrowser = errors.New("no browser found to show the window")
