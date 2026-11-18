// Package system is everything that touches the operating system: mounting
// drives, checking and installing WinFsp and SSHFS-Win, secret storage and
// connectivity tests. Only the Windows files talk to Windows; the rest is
// portable so it can be unit tested anywhere.
package system

import "context"

// ModuleInfo is the state of one required component.
type ModuleInfo struct {
	Name            string `json:"name"`
	Installed       bool   `json:"installed"`
	Path            string `json:"path,omitempty"`
	Version         string `json:"version,omitempty"` // installed version, when Windows reports one
	Latest          string `json:"latest,omitempty"`  // newest release on GitHub, when checked
	UpdateAvailable bool   `json:"updateAvailable"`
	ReleaseURL      string `json:"releaseUrl,omitempty"`
	Detail          string `json:"detail,omitempty"`
}

// ModuleStatus is the state of everything the app needs installed.
type ModuleStatus struct {
	Platform  string     `json:"platform"`
	Supported bool       `json:"supported"` // false when not running on Windows
	WinFsp    ModuleInfo `json:"winfsp"`
	SSHFSWin  ModuleInfo `json:"sshfsWin"`
	Ready     bool       `json:"ready"`

	Checked          bool   `json:"checked"` // the latest releases were looked up
	UpdatesAvailable bool   `json:"updatesAvailable"`
	LatestError      string `json:"latestError,omitempty"`
}

// MountRequest asks for one drive letter to be mapped.
type MountRequest struct {
	Letter     string
	UNC        string
	User       string
	Password   string
	Persistent bool
}

// Mappings describes what Windows currently has mapped.
type Mappings struct {
	SSHFS       map[string]string `json:"sshfs"`       // drive letter -> UNC, SSHFS-Win mappings only
	UsedLetters []string          `json:"usedLetters"` // every drive letter in use (any kind of drive)
}

// System is the operating-system surface the server needs.
type System interface {
	Modules(ctx context.Context) ModuleStatus
	// ModulesWithLatest also looks up the newest releases (online, cached for
	// an hour unless force is set).
	ModulesWithLatest(ctx context.Context, force bool) ModuleStatus
	// Install downloads and installs whatever is missing and, when update is
	// set, whatever is older than the latest release. log receives progress lines.
	Install(ctx context.Context, update bool, log func(string)) error
	Mount(ctx context.Context, r MountRequest) error
	Unmount(ctx context.Context, letter string) error
	Mappings(ctx context.Context) (Mappings, error)
	// SelfTest checks that the installed components are present and usable.
	SelfTest(ctx context.Context) []Step
	// Shortcut creates (or removes) a Windows shortcut to this program.
	Shortcut(ctx context.Context, sc Shortcut) (string, error)
	// Demo is true when the system is simulated (not Windows, or --demo).
	Demo() bool
}

// ShortcutFolder is a special folder name understood by
// [Environment]::GetFolderPath.
type ShortcutFolder string

const (
	FolderDesktop ShortcutFolder = "Desktop"
	FolderStartup ShortcutFolder = "Startup"
)

// Shortcut describes a .lnk that starts this program with arguments.
type Shortcut struct {
	Folder      ShortcutFolder
	FileName    string // without .lnk
	Args        string
	Description string
	Remove      bool
}

// Secrets stores one SSH password per device ID.
type Secrets interface {
	Set(id, password string) error
	Get(id string) (string, bool, error)
	Delete(id string) error
	// Secure reports whether secrets are encrypted at rest.
	Secure() bool
}
