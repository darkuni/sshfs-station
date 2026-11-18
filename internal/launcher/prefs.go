package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DisableBrowserExtras switches off the password manager and form autofill in
// the private browser profile the app window uses. Without this, Edge treats
// the device password box like a website login and offers to save it. Only
// this app's own profile (under the app's data folder) is changed; the
// person's normal Edge profile is never touched.
func DisableBrowserExtras(userDataDir string) error {
	path := filepath.Join(userDataDir, "Default", "Preferences")
	prefs := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(raw, &prefs) != nil {
			prefs = map[string]any{} // unreadable: start clean, it only holds window settings
		}
	}
	set := func(value any, keys ...string) {
		m := prefs
		for _, k := range keys[:len(keys)-1] {
			next, ok := m[k].(map[string]any)
			if !ok {
				next = map[string]any{}
				m[k] = next
			}
			m = next
		}
		m[keys[len(keys)-1]] = value
	}
	set(false, "credentials_enable_service")
	set(false, "credentials_enable_autosignin")
	set(false, "profile", "password_manager_enabled")
	set(false, "autofill", "enabled")
	set(false, "autofill", "profile_enabled")
	set(false, "autofill", "credit_card_enabled")
	set(false, "translate", "enabled")

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}
