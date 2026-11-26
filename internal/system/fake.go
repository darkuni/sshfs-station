package system

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// Fake is a simulated System. It backs --demo mode (and is what the app uses
// on non-Windows machines) and makes the rest of the code unit-testable.
type Fake struct {
	mu        sync.Mutex
	Delay     time.Duration
	WinFspOK  bool
	SSHFSOK   bool
	WinFspVer string // installed versions
	SSHFSVer  string
	LatestWF  string // newest releases
	LatestSS  string
	LatestErr error
	Mounted   map[string]string // letter -> UNC
	Other     []string          // letters used by non-SSHFS drives
	FailMount error             // when set, Mount returns it
	Calls     []string          // log of calls, for tests
}

// NewFake returns a Fake with everything installed and C: in use.
func NewFake() *Fake {
	return &Fake{WinFspOK: true, SSHFSOK: true, WinFspVer: "2.0.23075", SSHFSVer: "3.5.20357",
		LatestWF: "2.1.25156", LatestSS: "3.5.20357", Mounted: map[string]string{}, Other: []string{"C"}}
}

func (f *Fake) sleep(ctx context.Context) error {
	if f.Delay <= 0 {
		return nil
	}
	select {
	case <-time.After(f.Delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *Fake) Demo() bool { return true }

func (f *Fake) Modules(ctx context.Context) ModuleStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	ms := ModuleStatus{Platform: "demo", Supported: true}
	ms.WinFsp = ModuleInfo{Name: "WinFsp", Installed: f.WinFspOK, Path: `C:\Program Files (x86)\WinFsp`}
	ms.SSHFSWin = ModuleInfo{Name: "SSHFS-Win", Installed: f.SSHFSOK, Path: `C:\Program Files\SSHFS-Win`}
	if f.WinFspOK {
		ms.WinFsp.Version = f.WinFspVer
	}
	if f.SSHFSOK {
		ms.SSHFSWin.Version = f.SSHFSVer
	}
	ms.Ready = f.WinFspOK && f.SSHFSOK
	return ms
}

func (f *Fake) ModulesWithLatest(ctx context.Context, force bool) ModuleStatus {
	ms := f.Modules(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	ms.Checked = true
	if f.LatestErr != nil {
		ms.LatestError = "Could not check for updates: " + f.LatestErr.Error()
		return ms
	}
	ApplyLatest(&ms.WinFsp, f.LatestWF, "https://github.com/winfsp/winfsp/releases")
	ApplyLatest(&ms.SSHFSWin, f.LatestSS, "https://github.com/winfsp/sshfs-win/releases")
	ms.UpdatesAvailable = ms.WinFsp.UpdateAvailable || ms.SSHFSWin.UpdateAvailable
	return ms
}

func (f *Fake) Install(ctx context.Context, update bool, log func(string)) error {
	ms := f.Modules(ctx)
	if update {
		ms = f.ModulesWithLatest(ctx, true)
		if ms.LatestError != "" {
			return errors.New(ms.LatestError)
		}
	}
	needWF := !ms.WinFsp.Installed || ms.WinFsp.UpdateAvailable
	needSS := !ms.SSHFSWin.Installed || ms.SSHFSWin.UpdateAvailable
	if !needWF && !needSS {
		log("WinFsp and SSHFS-Win are up to date.")
		return nil
	}
	for _, j := range []struct {
		on   bool
		name string
	}{{needWF, "WinFsp"}, {needSS, "SSHFS-Win"}} {
		if !j.on {
			continue
		}
		for _, step := range []string{"Looking up the latest " + j.name + " release ...", "Downloading " + j.name + " (demo) ...", "Installing " + j.name + " (demo) ..."} {
			if err := f.sleep(ctx); err != nil {
				return err
			}
			log(step)
		}
	}
	f.mu.Lock()
	if needWF {
		f.WinFspOK, f.WinFspVer = true, f.LatestWF
	}
	if needSS {
		f.SSHFSOK, f.SSHFSVer = true, f.LatestSS
	}
	f.mu.Unlock()
	log("Done. (Demo mode: nothing was really installed.)")
	return nil
}

func (f *Fake) Mount(ctx context.Context, r MountRequest) error {
	if err := f.sleep(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "mount "+r.Letter)
	if f.FailMount != nil {
		return f.FailMount
	}
	if !f.WinFspOK || !f.SSHFSOK {
		return errors.New("WinFsp and SSHFS-Win must be installed first")
	}
	if _, used := f.Mounted[r.Letter]; used {
		return errors.New(FriendlyNetUseError("System error 85 has occurred."))
	}
	for _, o := range f.Other {
		if o == r.Letter {
			return errors.New(FriendlyNetUseError("System error 85 has occurred."))
		}
	}
	if r.Password == "" {
		return errors.New(FriendlyNetUseError("System error 1326 has occurred."))
	}
	f.Mounted[r.Letter] = r.UNC
	return nil
}

func (f *Fake) Unmount(ctx context.Context, letter string) error {
	if err := f.sleep(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "unmount "+letter)
	if _, ok := f.Mounted[letter]; !ok {
		return errors.New(FriendlyNetUseError("System error 2250 has occurred."))
	}
	delete(f.Mounted, letter)
	return nil
}

func (f *Fake) FlushDNS(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "flushdns")
	return nil
}

func (f *Fake) SelfTest(ctx context.Context) []Step {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []Step{
		{"WinFsp is installed", f.WinFspOK, pick(f.WinFspOK, "Found (demo).", "Not found.")},
		{"SSHFS-Win is installed", f.SSHFSOK, pick(f.SSHFSOK, "Found (demo).", "Not found.")},
		{"WinFsp launcher service is registered", f.WinFspOK, pick(f.WinFspOK, "Registered (demo).", "Not registered.")},
	}
}

func pick(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}

func (f *Fake) Shortcut(ctx context.Context, sc Shortcut) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	verb := "create"
	if sc.Remove {
		verb = "remove"
	}
	f.Calls = append(f.Calls, "shortcut "+verb+" "+string(sc.Folder)+" "+sc.FileName+" "+sc.Args)
	return `C:\Users\demo\` + string(sc.Folder) + `\` + sc.FileName + ".lnk", nil
}

func (f *Fake) Mappings(ctx context.Context) (Mappings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := Mappings{SSHFS: map[string]string{}}
	for l, u := range f.Mounted {
		m.SSHFS[l] = u
		m.UsedLetters = append(m.UsedLetters, l)
	}
	m.UsedLetters = append(m.UsedLetters, f.Other...)
	sort.Strings(m.UsedLetters)
	return m, nil
}

// MemSecrets is an in-memory Secrets used by tests and demo mode.
type MemSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func NewMemSecrets() *MemSecrets { return &MemSecrets{m: map[string]string{}} }

func (s *MemSecrets) Set(id, pw string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(id) == "" {
		return errors.New("empty id")
	}
	if pw == "" {
		delete(s.m, id)
		return nil
	}
	s.m[id] = pw
	return nil
}

func (s *MemSecrets) Get(id string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[id]
	return v, ok, nil
}

func (s *MemSecrets) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}

func (s *MemSecrets) Secure() bool { return false }
