package system

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	versionRe  = regexp.MustCompile(`\d+(?:\.\d+)+`)
	digitRunRe = regexp.MustCompile(`\d+`)
)

// VersionFromAsset pulls the version out of an installer name, for example
// "winfsp-2.1.25156.msi" -> "2.1.25156", "sshfs-win-3.5.20357-x64.msi" -> "3.5.20357".
func VersionFromAsset(name string) string {
	return versionRe.FindString(name)
}

// CompareVersions compares dotted versions number by number ("2.0.23075" vs
// "2.1.25156"). Missing parts count as 0. ok is false when either side has no
// numbers to compare.
func CompareVersions(a, b string) (cmp int, ok bool) {
	pa, pb := digitRunRe.FindAllString(a, -1), digitRunRe.FindAllString(b, -1)
	if len(pa) == 0 || len(pb) == 0 {
		return 0, false
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		switch {
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		}
	}
	return 0, true
}

// ApplyLatest records the newest release on a module and works out whether an
// update is available. An installed copy whose version cannot be read is not
// flagged as outdated.
func ApplyLatest(m *ModuleInfo, latest, releaseURL string) {
	m.Latest, m.ReleaseURL = latest, releaseURL
	m.UpdateAvailable = false
	if m.Installed && m.Version != "" && latest != "" {
		if c, ok := CompareVersions(m.Version, latest); ok && c < 0 {
			m.UpdateAvailable = true
		}
	}
}

// ParseUninstall reads "DisplayName|DisplayVersion" lines from the Windows
// uninstall list and returns the installed WinFsp and SSHFS-Win versions.
// When several entries exist, the highest version wins.
func ParseUninstall(out string) (winfsp, sshfs string) {
	keep := func(cur, v string) string {
		if cur == "" {
			return v
		}
		if c, ok := CompareVersions(cur, v); ok && c < 0 {
			return v
		}
		return cur
	}
	for _, line := range strings.Split(out, "\n") {
		name, ver, ok := strings.Cut(strings.TrimSpace(line), "|")
		ver = strings.TrimSpace(ver)
		if !ok || ver == "" {
			continue
		}
		n := strings.ToLower(strings.TrimSpace(name))
		switch {
		case strings.HasPrefix(n, "winfsp"):
			winfsp = keep(winfsp, ver)
		case strings.HasPrefix(n, "sshfs-win"):
			sshfs = keep(sshfs, ver)
		}
	}
	return
}

// LatestInfo is the newest release of one component.
type LatestInfo struct {
	Version string
	URL     string // release page
}

// LatestReleases looks up the newest WinFsp and SSHFS-Win releases on GitHub,
// remembering the answer for an hour so the API's rate limit is never an issue.
type LatestReleases struct {
	Inst *Installer
	TTL  time.Duration

	mu     sync.Mutex
	at     time.Time
	winfsp LatestInfo
	sshfs  LatestInfo
}

// Get returns the newest releases; force skips the cache.
func (l *LatestReleases) Get(ctx context.Context, force bool) (winfsp, sshfs LatestInfo, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ttl := l.TTL
	if ttl == 0 {
		ttl = time.Hour
	}
	if !force && !l.at.IsZero() && time.Since(l.at) < ttl {
		return l.winfsp, l.sshfs, nil
	}
	get := func(repo string) (LatestInfo, error) {
		rel, err := l.Inst.Latest(ctx, repo)
		if err != nil {
			return LatestInfo{}, err
		}
		a, err := PickAsset(repo, l.Inst.Arch, rel.Assets)
		if err != nil {
			return LatestInfo{}, err
		}
		v := VersionFromAsset(a.Name)
		if v == "" {
			v = strings.TrimPrefix(rel.Tag, "v")
		}
		return LatestInfo{Version: v, URL: rel.URL}, nil
	}
	w, err := get(repoWinFsp)
	if err != nil {
		return LatestInfo{}, LatestInfo{}, err
	}
	s, err := get(repoSSHFS)
	if err != nil {
		return LatestInfo{}, LatestInfo{}, err
	}
	l.winfsp, l.sshfs, l.at = w, s, time.Now()
	return w, s, nil
}

// WithLatest adds the newest release versions to a module status.
func WithLatest(ctx context.Context, ms ModuleStatus, l *LatestReleases, force bool) ModuleStatus {
	w, s, err := l.Get(ctx, force)
	ms.Checked = true
	if err != nil {
		ms.LatestError = "Could not check for updates: " + err.Error()
		return ms
	}
	ApplyLatest(&ms.WinFsp, w.Version, w.URL)
	ApplyLatest(&ms.SSHFSWin, s.Version, s.URL)
	ms.UpdatesAvailable = ms.WinFsp.UpdateAvailable || ms.SSHFSWin.UpdateAvailable
	return ms
}
