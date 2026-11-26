package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/darkuni/sshfs-station/internal/app"
	"github.com/darkuni/sshfs-station/internal/core"
	"github.com/darkuni/sshfs-station/internal/system"
)

type harness struct {
	r        *Runner
	out, err *bytes.Buffer
	fake     *system.Fake
	notes    []string
}

func setup(t *testing.T) *harness {
	t.Helper()
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	f := system.NewFake()
	svc := &app.Service{Store: st, Secrets: system.NewMemSecrets(), Sys: f}
	h := &harness{out: &bytes.Buffer{}, err: &bytes.Buffer{}, fake: f}
	h.r = &Runner{Svc: svc, Out: h.out, Err: h.err, Version: "1.2.3", Interactive: true,
		Notify: func(title, msg string, isErr bool) { h.notes = append(h.notes, msg) }}
	for _, x := range []struct {
		name string
		m    core.Model
		host string
	}{{"Living room Deck", core.ModelDeckOLED, "deck-lr"}, {"Travel Deck", core.ModelDeckLCD, "deck-tr"}, {"Steam Frame", core.ModelSteamFrame, "frame"}} {
		d := core.NewDevice(core.NewID(), x.m, st.List())
		d.Name, d.Host, d.SDCard = x.name, x.host, "sdcard"
		pw := "pw"
		if _, err := svc.Save(d, &pw); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func (h *harness) run(args ...string) int {
	h.out.Reset()
	h.err.Reset()
	return h.r.Run(context.Background(), args)
}

func TestBareNameMounts(t *testing.T) {
	h := setup(t)
	if code := h.run("Living room Deck"); code != OK {
		t.Fatalf("code %d: %s", code, h.err)
	}
	if len(h.fake.Mounted) != 3 || !strings.Contains(h.fake.Mounted["X"], "deck-lr") {
		t.Fatalf("mounted: %+v", h.fake.Mounted)
	}
	if !strings.Contains(h.out.String(), "Mounting Living room Deck") {
		t.Errorf("output: %s", h.out)
	}
}

func TestPrefixAndCase(t *testing.T) {
	h := setup(t)
	if code := h.run("mount", "living"); code != OK {
		t.Fatalf("prefix should work: %s", h.err)
	}
	if code := h.run("mount", "travel deck", "--letters", "w,u"); code != OK {
		t.Fatalf("code %d %s", code, h.err)
	}
	if _, ok := h.fake.Mounted["W"]; !ok || len(h.fake.Mounted) != 5 {
		t.Fatalf("letters filter: %+v", h.fake.Mounted)
	}
}

func TestErrorsAndExitCodes(t *testing.T) {
	h := setup(t)
	if code := h.run("mount", "nope"); code != Failed || !strings.Contains(h.err.String(), "Your profiles") {
		t.Errorf("unknown: %d %s", code, h.err)
	}
	if code := h.run("mount", "deck"); code != Failed || !strings.Contains(h.err.String(), "more than one") {
		t.Errorf("ambiguous: %d %s", code, h.err)
	}
	if code := h.run("mount"); code != Failed {
		t.Errorf("missing name: %d", code)
	}
	if code := h.run("mount", "living", "--bogus"); code != BadUsage {
		t.Errorf("bad flag: %d", code)
	}
	if code := h.run("mount", "living", "--letters"); code != BadUsage {
		t.Errorf("flag without value: %d", code)
	}
	h.fake.SSHFSOK = false
	if code := h.run("mount", "living"); code != Failed || !strings.Contains(h.err.String(), "setup") {
		t.Errorf("modules missing: %d %s", code, h.err)
	}
	h.fake.SSHFSOK = true
	h.fake.FailMount = errors.New("nope")
	if code := h.run("living"); code != Failed {
		t.Errorf("drive failure should exit 1, got %d", code)
	}
}

func TestUnmountAndStatus(t *testing.T) {
	h := setup(t)
	h.run("living")
	h.run("travel")
	if code := h.run("status", "--json"); code != OK {
		t.Fatal(h.err)
	}
	var st map[string][]app.DriveResult
	if err := json.Unmarshal(h.out.Bytes(), &st); err != nil {
		t.Fatalf("status json: %v\n%s", err, h.out)
	}
	if st["Living room Deck"][0].State != "mounted" || st["Steam Frame"][0].State != "unmounted" {
		t.Errorf("status: %+v", st)
	}
	if code := h.run("unmount", "living"); code != OK || len(h.fake.Mounted) != 3 {
		t.Fatalf("unmount one: %d %+v", code, h.fake.Mounted)
	}
	if code := h.run("unmount", "--all"); code != OK || len(h.fake.Mounted) != 0 {
		t.Fatalf("unmount all: %d %+v", code, h.fake.Mounted)
	}
}

func TestListExportHelp(t *testing.T) {
	h := setup(t)
	h.run("list")
	for _, n := range []string{"Living room Deck", "Steam Deck (OLED)", "Steam Frame", "X: Y: Z:"} {
		if !strings.Contains(h.out.String(), n) {
			t.Errorf("list missing %q:\n%s", n, h.out)
		}
	}
	out := filepath.Join(t.TempDir(), "m.bat")
	if code := h.run("export", "living", "--out", out); code != OK {
		t.Fatal(h.err)
	}
	b, _ := os.ReadFile(out)
	if !strings.Contains(string(b), `set "machine=deck-lr"`) {
		t.Errorf("bat: %s", b)
	}
	h.run("help")
	if !strings.Contains(h.out.String(), "sshfs-station \"<profile>\"") {
		t.Error("help text")
	}
	h.run("version")
	if strings.TrimSpace(h.out.String()) != "sshfs-station 1.2.3" {
		t.Errorf("version: %q", h.out)
	}
	if code := h.run(); code != OK {
		t.Error("no args prints usage")
	}
}

func TestCheckSetupTest(t *testing.T) {
	h := setup(t)
	h.fake.WinFspOK, h.fake.SSHFSOK = false, false
	if code := h.run("check"); code != Failed || !strings.Contains(h.out.String(), "sshfs-station setup") {
		t.Errorf("check: %d %s", code, h.out)
	}
	if code := h.run("setup"); code != OK || !h.fake.SSHFSOK {
		t.Errorf("setup: %d %s %s", code, h.out, h.err)
	}
	if code := h.run("test", "frame"); code != Failed || !strings.Contains(h.out.String(), "[FAIL]") {
		t.Errorf("unreachable test host should fail: %d %s", code, h.out)
	}
}

func TestShortcutNotifications(t *testing.T) {
	h := setup(t)
	h.r.Interactive = false
	h.run("living")
	if len(h.notes) != 1 || !strings.Contains(h.notes[0], "X: Y: Z: mounted") {
		t.Errorf("success note: %v", h.notes)
	}
	h.notes = nil
	h.run("travel", "--quiet")
	if len(h.notes) != 0 {
		t.Errorf("--quiet should not notify on success: %v", h.notes)
	}
	h.run("nope")
	if len(h.notes) != 1 {
		t.Errorf("failure should notify: %v", h.notes)
	}
}

func TestMountAutoCLI(t *testing.T) {
	h := setup(t)
	d, _ := h.r.Svc.FindDevice("frame")
	d.AutoMount = true
	_, _ = h.r.Svc.Save(d, nil)
	if code := h.run("mount", "--auto"); code != OK || len(h.fake.Mounted) != 3 {
		t.Fatalf("auto: %d %+v %s", code, h.fake.Mounted, h.err)
	}
}

func TestIsCommand(t *testing.T) {
	for _, c := range []string{"mount", "LIST", "--help", "/?"} {
		if !IsCommand(c) {
			t.Errorf("%s should be a command", c)
		}
	}
	if IsCommand("Living room Deck") {
		t.Error("profile name is not a command")
	}
}

func TestCheckShowsVersionsAndUpdate(t *testing.T) {
	h := setup(t)
	if code := h.run("check"); code != OK {
		t.Fatalf("check: %d %s", code, h.out)
	}
	out := h.out.String()
	for _, want := range []string{"WinFsp:    2.0.23075, update available: 2.1.25156", "SSHFS-Win: 3.5.20357, up to date", "sshfs-station update"} {
		if !strings.Contains(out, want) {
			t.Errorf("check output missing %q:\n%s", want, out)
		}
	}
	if code := h.run("update"); code != OK || h.fake.WinFspVer != "2.1.25156" {
		t.Fatalf("update: %d %s", code, h.out)
	}
	h.run("check")
	if !strings.Contains(h.out.String(), "WinFsp:    2.1.25156, up to date") {
		t.Errorf("after update:\n%s", h.out)
	}
	h.fake.LatestErr = errors.New("offline")
	h.run("check")
	if !strings.Contains(h.out.String(), "Could not check for updates") {
		t.Errorf("offline check should say so:\n%s", h.out)
	}
}

func TestFlushDNS(t *testing.T) {
	h := setup(t)
	if code := h.run("flushdns"); code != OK || !strings.Contains(h.out.String(), "Flushed the DNS cache") {
		t.Fatalf("flushdns: %d %s", code, h.out)
	}
	if h.fake.Calls[len(h.fake.Calls)-1] != "flushdns" {
		t.Errorf("system flush not called: %v", h.fake.Calls)
	}
	if !IsCommand("flushdns") {
		t.Error("flushdns must not be treated as a profile name")
	}
}
