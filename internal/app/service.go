// Package app is the one place that knows how to mount, unmount, test and
// export a device profile. The window (HTTP API) and the command line both
// call it, so they always behave the same.
package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/darkuni/sshfs-station/internal/core"
	"github.com/darkuni/sshfs-station/internal/system"
)

// Service ties together profiles, secrets and the operating system.
type Service struct {
	Store   *core.Store
	Secrets system.Secrets
	Sys     system.System

	mu     sync.Mutex
	mounts map[string]map[string]string // device ID -> letter -> UNC it was mounted with
}

// remember records what this app mounted, so a profile that is edited while
// mounted can still find (and unmount) its old mappings.
func (s *Service) remember(id, letter, unc string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mounts == nil {
		s.mounts = map[string]map[string]string{}
	}
	if s.mounts[id] == nil {
		s.mounts[id] = map[string]string{}
	}
	if unc == "" {
		delete(s.mounts[id], letter)
	} else {
		s.mounts[id][letter] = unc
	}
}

func (s *Service) remembered(id string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for l, u := range s.mounts[id] {
		out[l] = u
	}
	return out
}

// ValidationError means a device has problems that must be fixed first.
type ValidationError struct{ Issues []core.Issue }

func (e *ValidationError) Error() string {
	var msgs []string
	for _, i := range e.Issues {
		if i.Severity == core.Error {
			msgs = append(msgs, i.Message)
		}
	}
	return strings.Join(msgs, " ")
}

// ErrModulesMissing means WinFsp and/or SSHFS-Win are not installed.
var ErrModulesMissing = errors.New("WinFsp and SSHFS-Win must be installed first (Setup tab, or run: sshfs-station setup)")

// FindDevice looks a profile up by ID or by name. Matching is case-insensitive
// and goes from strict to loose: exact name, then unique prefix, then unique
// substring. Ambiguous or missing names return a helpful error.
func (s *Service) FindDevice(query string) (core.Device, error) {
	q := strings.TrimSpace(query)
	list := s.Store.List()
	if q == "" {
		return core.Device{}, errors.New("no profile name given")
	}
	for _, d := range list {
		if d.ID == q {
			return d, nil
		}
	}
	lq := strings.ToLower(q)
	var exact, prefix, contains []core.Device
	for _, d := range list {
		n := strings.ToLower(strings.TrimSpace(d.Name))
		switch {
		case n == lq:
			exact = append(exact, d)
		case strings.HasPrefix(n, lq):
			prefix = append(prefix, d)
		case strings.Contains(n, lq):
			contains = append(contains, d)
		}
	}
	for _, tier := range [][]core.Device{exact, prefix, contains} {
		if len(tier) == 1 {
			return tier[0], nil
		}
		if len(tier) > 1 {
			return core.Device{}, fmt.Errorf("\"%s\" matches more than one profile: %s", q, names(tier))
		}
	}
	if len(list) == 0 {
		return core.Device{}, fmt.Errorf("no profile called \"%s\" (you have no saved profiles yet)", q)
	}
	return core.Device{}, fmt.Errorf("no profile called \"%s\". Your profiles: %s", q, names(list))
}

func names(ds []core.Device) string {
	var n []string
	for _, d := range ds {
		n = append(n, "\""+d.Name+"\"")
	}
	return strings.Join(n, ", ")
}

func (s *Service) others(id string) []core.Device {
	var out []core.Device
	for _, d := range s.Store.List() {
		if d.ID != id {
			out = append(out, d)
		}
	}
	return out
}

// Validate checks a device against the rest of the profiles and the saved password.
func (s *Service) Validate(d core.Device) []core.Issue {
	_, have, _ := s.Secrets.Get(d.ID)
	return core.Validate(d, s.others(d.ID), have)
}

// DriveResult is the outcome (or state) of one drive.
type DriveResult struct {
	Letter  string `json:"letter"`
	Label   string `json:"label"`
	UNC     string `json:"unc"`
	State   string `json:"state"`            // mounted, unmounted, conflict, failed
	Stale   bool   `json:"stale,omitempty"`  // mounted with settings that have since changed
	Mapped  string `json:"mapped,omitempty"` // what the letter is really mapped to, when different
	Message string `json:"message,omitempty"`
}

// StaleMessage explains a mapping made with old settings.
const StaleMessage = "Mounted with older settings. Mount again to apply your changes."

// OldLabel marks a mapping whose letter is no longer in the profile.
const OldLabel = "Old mapping"

func wantLetter(letters []string, l string) bool {
	if len(letters) == 0 {
		return true
	}
	for _, x := range letters {
		if strings.EqualFold(x, l) {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Status reports, for each active drive of a device, whether it is mounted.
func (s *Service) Status(ctx context.Context, d core.Device) ([]DriveResult, error) {
	m, err := s.Sys.Mappings(ctx)
	if err != nil {
		return nil, err
	}
	return s.statusWith(d, m), nil
}

// StatusAll reports the drive status of every device with one look at Windows.
func (s *Service) StatusAll(ctx context.Context) (map[string][]DriveResult, system.Mappings, error) {
	m, err := s.Sys.Mappings(ctx)
	if err != nil {
		return nil, m, err
	}
	out := map[string][]DriveResult{}
	for _, d := range s.Store.List() {
		out[d.ID] = s.statusWith(d, m)
	}
	return out, m, nil
}

func (s *Service) statusWith(d core.Device, m system.Mappings) []DriveResult {
	rec := s.remembered(d.ID)
	out := []DriveResult{}
	seen := map[string]bool{}
	for _, dr := range core.ActiveDrives(d) {
		seen[dr.Letter] = true
		unc, err := core.UNC(d, dr)
		r := DriveResult{Letter: dr.Letter, Label: core.LocationLabel(dr.Location), UNC: unc}
		mapped := m.SSHFS[dr.Letter]
		switch {
		case err != nil:
			r.State, r.Message = "failed", err.Error()
		case system.SameUNC(mapped, unc):
			r.State = "mounted"
			if rec[dr.Letter] != unc {
				s.remember(d.ID, dr.Letter, unc) // e.g. mounted before the app started
			}
		case mapped != "" && system.SameUNC(mapped, rec[dr.Letter]):
			r.State, r.Stale, r.Mapped, r.Message = "mounted", true, mapped, StaleMessage
		case contains(m.UsedLetters, dr.Letter):
			r.State, r.Message = "conflict", "Drive letter "+dr.Letter+": is in use by something else."
		default:
			r.State = "unmounted"
		}
		out = append(out, r)
	}
	// Mappings this app made for the device on letters it no longer uses.
	var old []string
	for l, u := range rec {
		if !seen[l] && system.SameUNC(m.SSHFS[l], u) {
			old = append(old, l)
		}
	}
	sort.Strings(old)
	for _, l := range old {
		out = append(out, DriveResult{Letter: l, Label: OldLabel, UNC: rec[l], Mapped: rec[l], State: "mounted", Stale: true,
			Message: "No longer in this profile but still mounted. Unmount it to free the letter."})
	}
	return out
}

// Mount maps the device's drives (all of them, or only the given letters).
func (s *Service) Mount(ctx context.Context, d core.Device, letters ...string) ([]DriveResult, error) {
	pw, have, err := s.Secrets.Get(d.ID)
	if err != nil {
		return nil, err
	}
	if issues := core.Validate(d, s.others(d.ID), have); core.HasErrors(issues) {
		return nil, &ValidationError{issues}
	}
	if !s.Sys.Modules(ctx).Ready {
		return nil, ErrModulesMissing
	}
	m, err := s.Sys.Mappings(ctx)
	if err != nil {
		return nil, err
	}
	rec := s.remembered(d.ID)
	var out []DriveResult
	for _, dr := range core.ActiveDrives(d) {
		if !wantLetter(letters, dr.Letter) {
			continue
		}
		unc, _ := core.UNC(d, dr)
		r := DriveResult{Letter: dr.Letter, Label: core.LocationLabel(dr.Location), UNC: unc}
		mapped := m.SSHFS[dr.Letter]
		stale := mapped != "" && !system.SameUNC(mapped, unc) && system.SameUNC(mapped, rec[dr.Letter])
		switch {
		case system.SameUNC(mapped, unc):
			s.remember(d.ID, dr.Letter, unc)
			r.State, r.Message = "mounted", "Already mounted."
		case contains(m.UsedLetters, dr.Letter) && !stale:
			r.State, r.Message = "failed", "Drive letter "+dr.Letter+": is already in use. Pick another letter."
		default:
			if stale { // mounted earlier with old settings: replace it
				uctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				err := s.Sys.Unmount(uctx, dr.Letter)
				cancel()
				if err != nil {
					r.State, r.Message = "failed", "Could not replace the old mapping: "+err.Error()
					out = append(out, r)
					continue
				}
				s.remember(d.ID, dr.Letter, "")
			}
			mctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			err := s.Sys.Mount(mctx, system.MountRequest{
				Letter: dr.Letter, UNC: unc, User: strings.TrimSpace(d.Username), Password: pw,
			})
			cancel()
			if err != nil {
				r.State, r.Message = "failed", err.Error()
			} else {
				r.State = "mounted"
				s.remember(d.ID, dr.Letter, unc)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// Unmount removes the device's drives. It only touches letters that really
// are mapped to this device (with its current or remembered settings), so
// another device on the same letter is left alone.
func (s *Service) Unmount(ctx context.Context, d core.Device, letters ...string) ([]DriveResult, error) {
	m, err := s.Sys.Mappings(ctx)
	if err != nil {
		return nil, err
	}
	var out []DriveResult
	for _, st := range s.statusWith(d, m) {
		if !wantLetter(letters, st.Letter) {
			continue
		}
		r := DriveResult{Letter: st.Letter, Label: st.Label, UNC: st.UNC}
		if st.State != "mounted" {
			r.State, r.Message = "unmounted", "Not mounted."
			out = append(out, r)
			continue
		}
		uctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := s.Sys.Unmount(uctx, st.Letter)
		cancel()
		if err != nil {
			r.State, r.Message = "failed", err.Error()
		} else {
			r.State = "unmounted"
			s.remember(d.ID, st.Letter, "")
		}
		out = append(out, r)
	}
	return out, nil
}

// Export renders the .bat for a device using its saved password.
func (s *Service) Export(d core.Device) (string, error) {
	pw, _, err := s.Secrets.Get(d.ID)
	if err != nil {
		return "", err
	}
	if issues := s.Validate(d); core.HasErrors(issues) {
		return "", &ValidationError{issues}
	}
	return core.ExportBatch(d, pw)
}

// Test checks connectivity to a device.
func (s *Service) Test(ctx context.Context, d core.Device) []system.Step {
	return system.TestConnection(ctx, d.Host, d.EffectivePort())
}

// Save stores the device (drafts with problems are allowed) and the password
// when one is supplied (nil leaves the saved password unchanged). It keeps the
// sign-in shortcut in step with the devices set to mount automatically.
func (s *Service) Save(d core.Device, password *string) ([]core.Issue, error) {
	old, oldErr := s.Store.Get(d.ID)
	if err := s.Store.Put(d); err != nil {
		return nil, err
	}
	if password != nil {
		if err := s.Secrets.Set(d.ID, *password); err != nil {
			return nil, err
		}
	}
	if (oldErr == nil && old.AutoMount != d.AutoMount) || (oldErr != nil && d.AutoMount) {
		if err := s.SyncAutostart(context.Background()); err != nil {
			return s.Validate(d), err
		}
	}
	return s.Validate(d), nil
}

// AutostartArgs are the command-line arguments the sign-in shortcut uses.
const AutostartArgs = "mount --auto"

// SyncAutostart creates the sign-in shortcut when any device should mount
// automatically, and removes it when none should.
func (s *Service) SyncAutostart(ctx context.Context) error {
	want := false
	for _, d := range s.Store.List() {
		want = want || d.AutoMount
	}
	_, err := s.Sys.Shortcut(ctx, system.Shortcut{
		Folder: system.FolderStartup, FileName: "SSHFS Station autostart", Args: AutostartArgs,
		Description: "Mount SSHFS Station devices when you sign in", Remove: !want,
	})
	if err != nil {
		return err
	}
	// Remove the shortcut made under the app's earlier name, if any.
	_, _ = s.Sys.Shortcut(ctx, system.Shortcut{Folder: system.FolderStartup, FileName: "sshfs-master autostart", Remove: true})
	return nil
}

// MountAuto mounts every device that has AutoMount switched on.
func (s *Service) MountAuto(ctx context.Context) map[string][]DriveResult {
	out := map[string][]DriveResult{}
	for _, d := range s.Store.List() {
		if !d.AutoMount {
			continue
		}
		res, err := s.Mount(ctx, d)
		if err != nil {
			res = []DriveResult{{State: "failed", Message: err.Error()}}
		}
		out[d.Name] = res
	}
	return out
}

var badFileChars = strings.NewReplacer(`\`, "-", "/", "-", ":", "-", "*", "", "?", "", `"`, "", "<", "", ">", "", "|", "")

// DesktopShortcut puts a shortcut on the desktop that mounts this device
// without opening the window.
func (s *Service) DesktopShortcut(ctx context.Context, d core.Device) (string, error) {
	return s.Sys.Shortcut(ctx, system.Shortcut{
		Folder: system.FolderDesktop, FileName: "Mount " + strings.TrimSpace(badFileChars.Replace(d.Name)),
		Args: `mount "` + strings.TrimSpace(d.Name) + `"`, Description: "Mount the drives of " + d.Name,
	})
}

// Delete removes a device and its saved password.
func (s *Service) Delete(id string) error {
	old, err := s.Store.Get(id)
	if err != nil {
		return err
	}
	if err := s.Store.Delete(id); err != nil {
		return err
	}
	if err := s.Secrets.Delete(id); err != nil {
		return err
	}
	if old.AutoMount {
		return s.SyncAutostart(context.Background())
	}
	return nil
}

// Duplicate copies a device (without its password) under a new unique name,
// moving its drives to free letters so both can be mounted together.
func (s *Service) Duplicate(id string) (core.Device, error) {
	src, err := s.Store.Get(id)
	if err != nil {
		return core.Device{}, err
	}
	all := s.Store.List()
	cp := src
	cp.ID = core.NewID()
	taken := map[string]bool{}
	for _, d := range all {
		taken[strings.ToLower(d.Name)] = true
	}
	cp.Name = src.Name + " copy"
	for i := 2; taken[strings.ToLower(cp.Name)]; i++ {
		cp.Name = fmt.Sprintf("%s copy %d", src.Name, i)
	}
	cp.AutoMount = false
	cp.Drives = append([]core.Drive(nil), src.Drives...)
	free := core.SuggestLetters(all, len(cp.Drives))
	for i := range cp.Drives {
		if i < len(free) {
			cp.Drives[i].Letter = free[i]
		}
	}
	return cp, s.Store.Put(cp)
}

// SortedNames lists profile names, for the CLI.
func (s *Service) SortedNames() []string {
	var n []string
	for _, d := range s.Store.List() {
		n = append(n, d.Name)
	}
	sort.Strings(n)
	return n
}
