package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDataDirMovesOldName(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses XDG_CONFIG_HOME to point the config folder at a temp dir")
	}
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	old := filepath.Join(base, "sshfs-master")
	os.MkdirAll(old, 0o700)
	os.WriteFile(filepath.Join(old, "devices.json"), []byte(`{"version":1}`), 0o600)

	dir, err := defaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(base, "sshfs-station") {
		t.Fatalf("dir = %s", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "devices.json")); err != nil {
		t.Fatal("profiles should have moved from the old folder")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old folder should be gone")
	}

	// Never overwrite an existing new folder.
	os.MkdirAll(old, 0o700)
	if _, err := defaultDataDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("old folder must be left alone when the new one exists")
	}
}
