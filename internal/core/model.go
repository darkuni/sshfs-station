// Package core holds the platform-independent logic: device profiles,
// drive mappings, path building, validation and .bat export.
package core

// Model identifies the kind of Steam hardware a device profile represents.
type Model string

const (
	ModelDeckLCD      Model = "deck-lcd"
	ModelDeckOLED     Model = "deck-oled"
	ModelSteamMachine Model = "steam-machine"
	ModelSteamFrame   Model = "steam-frame"
	ModelOther        Model = "other"
)

// ModelInfo describes a hardware model and the defaults a new profile starts with.
// Defaults are only suggestions; every field stays editable.
type ModelInfo struct {
	ID          Model  `json:"id"`
	Label       string `json:"label"`
	DefaultHost string `json:"defaultHost"`
	DefaultUser string `json:"defaultUser"`
	HostHint    string `json:"hostHint"`
}

// Models lists the supported hardware, in the order the UI shows them.
func Models() []ModelInfo {
	return []ModelInfo{
		{ModelDeckLCD, "Steam Deck (LCD)", "steamdeck", "deck",
			"steamdeck is the default name. Use that unless you know you have changed it, or enter the IP address."},
		{ModelDeckOLED, "Steam Deck (OLED)", "steamdeck", "deck",
			"steamdeck is the default name. If you own more than one Deck, give each a different hostname or use its IP address."},
		{ModelSteamMachine, "Steam Machine", "", "deck",
			"Use the hostname or IP address of the Steam Machine (check your router's device list)."},
		{ModelSteamFrame, "Steam Frame", "", "deck",
			"Use the hostname or IP address of the Steam Frame (check your router's device list)."},
		{ModelOther, "Other SSH device", "", "", "Hostname or IP address of the device."},
	}
}

// ModelByID returns the info for a model, and false if it is unknown.
func ModelByID(id Model) (ModelInfo, bool) {
	for _, m := range Models() {
		if m.ID == id {
			return m, true
		}
	}
	return ModelInfo{}, false
}

// Location is where a mapped drive points on the remote device.
type Location string

const (
	LocHome   Location = "home"
	LocSD     Location = "sdcard"
	LocRoot   Location = "root"
	LocCustom Location = "custom"
)

// Drive maps one Windows drive letter to one remote location.
type Drive struct {
	Letter   string   `json:"letter"`
	Location Location `json:"location"`
	Custom   string   `json:"custom,omitempty"`
}

// Device is one saved device profile. The SSH password is never part of this
// struct: it is stored separately in the OS secret store.
type Device struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Model     Model   `json:"model"`
	Host      string  `json:"host"`
	Port      int     `json:"port"` // 0 means 22
	Username  string  `json:"username"`
	SDCard    string  `json:"sdCard"`
	NoSD      bool    `json:"noSd"`
	Drives    []Drive `json:"drives"`
	NoPauses  bool    `json:"noPauses"`
	AutoMount bool    `json:"autoMount"` // mount when you sign in to Windows
	Notes     string  `json:"notes,omitempty"`
}

// EffectivePort returns the SSH port, defaulting to 22.
func (d Device) EffectivePort() int {
	if d.Port <= 0 {
		return 22
	}
	return d.Port
}

// HasSD reports whether SD-card locations can be used on this device.
func (d Device) HasSD() bool {
	return !d.NoSD && trim(d.SDCard) != ""
}

// MaxDrives is the most drive mappings one device can hold.
const MaxDrives = 8

// preferredLetters is the order drive letters are handed out in. The first
// device gets X, Y, Z; the next gets W, V, U; and so on.
var preferredLetters = []string{"X", "Y", "Z", "W", "V", "U", "T", "S", "R", "Q", "P", "O", "N", "M", "L", "K", "J", "I", "H", "G", "F", "E", "D"}

// SuggestLetters returns n drive letters not used by any of the given devices.
func SuggestLetters(devices []Device, n int) []string {
	used := map[string]bool{}
	for _, d := range devices {
		for _, dr := range d.Drives {
			used[dr.Letter] = true
		}
	}
	var out []string
	for _, l := range preferredLetters {
		if len(out) == n {
			break
		}
		if !used[l] {
			out = append(out, l)
		}
	}
	return out
}

// NewDevice builds a fresh profile for a model with sensible defaults:
// Home, SD Card and Root drives on the next free letters.
func NewDevice(id string, model Model, existing []Device) Device {
	info, ok := ModelByID(model)
	if !ok {
		info, _ = ModelByID(ModelOther)
		model = ModelOther
	}
	letters := SuggestLetters(existing, 3)
	locs := []Location{LocHome, LocSD, LocRoot}
	var drives []Drive
	for i, l := range letters {
		drives = append(drives, Drive{Letter: l, Location: locs[i]})
	}
	return Device{
		ID:       id,
		Name:     uniqueName(info.Label, existing),
		Model:    model,
		Host:     info.DefaultHost,
		Username: info.DefaultUser,
		Drives:   drives,
	}
}

func uniqueName(base string, existing []Device) string {
	taken := map[string]bool{}
	for _, d := range existing {
		taken[d.Name] = true
	}
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		n := base + " " + itoa(i)
		if !taken[n] {
			return n
		}
	}
}
