package system

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestVersionFromAsset(t *testing.T) {
	for in, want := range map[string]string{
		"winfsp-2.1.25156.msi":        "2.1.25156",
		"sshfs-win-3.5.20357-x64.msi": "3.5.20357",
		"sshfs-win-3.7.21011-x86.msi": "3.7.21011",
		"readme.txt":                  "",
	} {
		if got := VersionFromAsset(in); got != want {
			t.Errorf("VersionFromAsset(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2.0.23075", "2.1.25156", -1},
		{"2.1.25156", "2.0.23075", 1},
		{"2.1", "2.1.0", 0},
		{"3.5.20357", "3.5.20357", 0},
		{"3.10.1", "3.9.9", 1}, // numeric, not alphabetical
		{"v2.0", "2.0", 0},
	}
	for _, c := range cases {
		got, ok := CompareVersions(c.a, c.b)
		if !ok || got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, %v; want %d", c.a, c.b, got, ok, c.want)
		}
	}
	if _, ok := CompareVersions("", "1.0"); ok {
		t.Error("empty version cannot be compared")
	}
}

func TestApplyLatest(t *testing.T) {
	m := ModuleInfo{Installed: true, Version: "2.0.23075"}
	ApplyLatest(&m, "2.1.25156", "u")
	if !m.UpdateAvailable || m.Latest != "2.1.25156" {
		t.Errorf("older install should need an update: %+v", m)
	}
	m = ModuleInfo{Installed: true, Version: "2.1.25156"}
	ApplyLatest(&m, "2.1.25156", "u")
	if m.UpdateAvailable {
		t.Error("same version needs no update")
	}
	m = ModuleInfo{Installed: true}
	ApplyLatest(&m, "2.1.25156", "u")
	if m.UpdateAvailable {
		t.Error("unknown installed version must not be flagged")
	}
	m = ModuleInfo{Installed: false}
	ApplyLatest(&m, "2.1.25156", "u")
	if m.UpdateAvailable {
		t.Error("missing is not 'update available', it is 'not installed'")
	}
}

func TestParseUninstall(t *testing.T) {
	out := "Some App|1.0\r\nWinFsp 2023|2.0.23075\r\nSSHFS-Win 2021.1 Beta2|3.5.20357\r\nWinFsp 2024|2.1.24255\r\nWinFsp broken|\r\n"
	wf, ss := ParseUninstall(out)
	if wf != "2.1.24255" || ss != "3.5.20357" {
		t.Errorf("got %q %q", wf, ss)
	}
	if wf, ss := ParseUninstall(""); wf != "" || ss != "" {
		t.Error("nothing installed")
	}
}

func TestLatestReleasesCacheAndErrors(t *testing.T) {
	g := newFakeGitHub(t)
	var calls int32
	inner := g.srv.Config.Handler
	g.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/releases/latest") {
			atomic.AddInt32(&calls, 1)
		}
		inner.ServeHTTP(w, r)
	})
	var none []string
	l := &LatestReleases{Inst: g.installer(&none)}
	w, s, err := l.Get(context.Background(), false)
	if err != nil || w.Version != "2.0" || s.Version != "3.5" {
		t.Fatalf("latest: %+v %+v %v", w, s, err)
	}
	_, _, _ = l.Get(context.Background(), false)
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("second lookup should come from the cache, calls=%d", calls)
	}
	_, _, _ = l.Get(context.Background(), true)
	if atomic.LoadInt32(&calls) != 4 {
		t.Errorf("force should skip the cache, calls=%d", calls)
	}

	ms := ModuleStatus{WinFsp: ModuleInfo{Installed: true, Version: "1.9"}, SSHFSWin: ModuleInfo{Installed: true, Version: "3.5"}}
	ms = WithLatest(context.Background(), ms, l, false)
	if !ms.Checked || !ms.UpdatesAvailable || !ms.WinFsp.UpdateAvailable || ms.SSHFSWin.UpdateAvailable {
		t.Errorf("with latest: %+v", ms)
	}

	bad := &LatestReleases{Inst: &Installer{APIBase: "http://127.0.0.1:1", Client: &http.Client{Timeout: time.Second}}}
	ms = WithLatest(context.Background(), ModuleStatus{}, bad, true)
	if ms.LatestError == "" || ms.UpdatesAvailable {
		t.Errorf("offline should report an error, not updates: %+v", ms)
	}
}

func TestFakeUpdateFlow(t *testing.T) {
	f := NewFake()
	ms := f.ModulesWithLatest(context.Background(), false)
	if !ms.WinFsp.UpdateAvailable || ms.SSHFSWin.UpdateAvailable {
		t.Fatalf("demo should show a WinFsp update: %+v", ms)
	}
	var log []string
	if err := f.Install(context.Background(), false, func(s string) { log = append(log, s) }); err != nil {
		t.Fatal(err)
	}
	if f.WinFspVer != "2.0.23075" {
		t.Error("plain setup must not update installed components")
	}
	if err := f.Install(context.Background(), true, func(s string) { log = append(log, s) }); err != nil {
		t.Fatal(err)
	}
	if f.WinFspVer != "2.1.25156" || f.ModulesWithLatest(context.Background(), false).UpdatesAvailable {
		t.Errorf("update should bring WinFsp to the latest: %s", f.WinFspVer)
	}
	f.LatestErr = errors.New("offline")
	if err := f.Install(context.Background(), true, func(string) {}); err == nil {
		t.Error("update without being able to check must fail")
	}
}
