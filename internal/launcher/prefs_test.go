package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readPrefs(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "Default", "Preferences"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDisableBrowserExtrasFresh(t *testing.T) {
	dir := t.TempDir()
	if err := DisableBrowserExtras(dir); err != nil {
		t.Fatal(err)
	}
	m := readPrefs(t, dir)
	if m["credentials_enable_service"] != false {
		t.Error("password saving should be off")
	}
	if m["profile"].(map[string]any)["password_manager_enabled"] != false {
		t.Error("password manager should be off")
	}
	if m["autofill"].(map[string]any)["profile_enabled"] != false {
		t.Error("autofill should be off")
	}
}

func TestDisableBrowserExtrasKeepsOtherSettings(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Default", "Preferences")
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte(`{"browser":{"window_placement":{"left":10}},"profile":{"name":"x","password_manager_enabled":true}}`), 0o600)
	if err := DisableBrowserExtras(dir); err != nil {
		t.Fatal(err)
	}
	m := readPrefs(t, dir)
	if m["browser"].(map[string]any)["window_placement"].(map[string]any)["left"] != float64(10) {
		t.Error("window position must be kept")
	}
	prof := m["profile"].(map[string]any)
	if prof["name"] != "x" || prof["password_manager_enabled"] != false {
		t.Errorf("profile merge wrong: %v", prof)
	}
	os.WriteFile(p, []byte(`not json`), 0o600)
	if err := DisableBrowserExtras(dir); err != nil {
		t.Fatal(err)
	}
	if readPrefs(t, dir)["credentials_enable_service"] != false {
		t.Error("corrupt file should be replaced")
	}
}
