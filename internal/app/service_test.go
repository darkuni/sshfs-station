package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/darkuni/sshfs-station/internal/core"
	"github.com/darkuni/sshfs-station/internal/system"
)

func newSvc(t *testing.T) (*Service, *system.Fake) {
	t.Helper()
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	f := system.NewFake()
	return &Service{Store: st, Secrets: system.NewMemSecrets(), Sys: f}, f
}

func addDevice(t *testing.T, s *Service, name string, model core.Model, host, pw string) core.Device {
	t.Helper()
	d := core.NewDevice(core.NewID(), model, s.Store.List())
	d.Name, d.Host, d.SDCard = name, host, "sdcard"
	issues, err := s.Save(d, &pw)
	if err != nil || core.HasErrors(issues) {
		t.Fatalf("save %s: %v %+v", name, err, issues)
	}
	return d
}

func TestFindDevice(t *testing.T) {
	s, _ := newSvc(t)
	a := addDevice(t, s, "Living room Deck", core.ModelDeckOLED, "deck-a", "pw")
	addDevice(t, s, "Travel Deck", core.ModelDeckLCD, "deck-b", "pw")
	addDevice(t, s, "Travel Frame", core.ModelSteamFrame, "frame", "pw")

	for q, want := range map[string]string{"living room deck": "Living room Deck", "Living": "Living room Deck", "room": "Living room Deck", "travel deck": "Travel Deck", a.ID: "Living room Deck"} {
		d, err := s.FindDevice(q)
		if err != nil || d.Name != want {
			t.Errorf("FindDevice(%q) = %q, %v; want %q", q, d.Name, err, want)
		}
	}
	if _, err := s.FindDevice("travel"); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Errorf("ambiguous prefix should error, got %v", err)
	}
	if _, err := s.FindDevice("nothing"); err == nil || !strings.Contains(err.Error(), "Living room Deck") {
		t.Errorf("unknown name should list profiles, got %v", err)
	}
	if _, err := s.FindDevice(" "); err == nil {
		t.Error("blank query should error")
	}
}

func TestMountAndUnmount(t *testing.T) {
	s, f := newSvc(t)
	d := addDevice(t, s, "Deck", core.ModelDeckLCD, "steamdeck", "pw")
	ctx := context.Background()

	res, err := s.Mount(ctx, d)
	if err != nil || len(res) != 3 {
		t.Fatalf("mount: %v %+v", err, res)
	}
	for _, r := range res {
		if r.State != "mounted" {
			t.Errorf("%s: want mounted, got %s %s", r.Letter, r.State, r.Message)
		}
	}
	if f.Mounted["X"] != `\\sshfs.r\deck@steamdeck\home\deck` || f.Mounted["Z"] != `\\sshfs.r\deck@steamdeck` {
		t.Errorf("wrong UNCs mounted: %+v", f.Mounted)
	}

	res, _ = s.Mount(ctx, d)
	for _, r := range res {
		if r.State != "mounted" || r.Message != "Already mounted." {
			t.Errorf("remount should be a no-op, got %+v", r)
		}
	}
	if n := countCalls(f, "mount"); n != 3 {
		t.Errorf("mount called %d times, want 3", n)
	}

	st, _ := s.Status(ctx, d)
	for _, r := range st {
		if r.State != "mounted" {
			t.Errorf("status %s = %s", r.Letter, r.State)
		}
	}

	res, _ = s.Unmount(ctx, d, "y")
	if len(res) != 1 || res[0].State != "unmounted" || res[0].Message != "" {
		t.Errorf("unmount Y: %+v", res)
	}
	if _, still := f.Mounted["Y"]; still || len(f.Mounted) != 2 {
		t.Errorf("only Y should be gone: %+v", f.Mounted)
	}
	res, _ = s.Unmount(ctx, d)
	if len(f.Mounted) != 0 {
		t.Errorf("all should be unmounted: %+v", f.Mounted)
	}
	_ = res
}

func countCalls(f *system.Fake, prefix string) int {
	n := 0
	for _, c := range f.Calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestMountOnlyRequestedLetters(t *testing.T) {
	s, f := newSvc(t)
	d := addDevice(t, s, "Deck", core.ModelDeckLCD, "steamdeck", "pw")
	res, _ := s.Mount(context.Background(), d, "Z")
	if len(res) != 1 || res[0].Letter != "Z" || len(f.Mounted) != 1 {
		t.Fatalf("got %+v / %+v", res, f.Mounted)
	}
}

func TestTwoDevicesDoNotInterfere(t *testing.T) {
	s, f := newSvc(t)
	a := addDevice(t, s, "Deck A", core.ModelDeckLCD, "deck-a", "pw")
	b := addDevice(t, s, "Deck B", core.ModelDeckOLED, "deck-b", "pw")
	ctx := context.Background()
	if _, err := s.Mount(ctx, a); err != nil {
		t.Fatal(err)
	}
	res, err := s.Mount(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.State != "mounted" {
			t.Errorf("device B drive %s: %s %s", r.Letter, r.State, r.Message)
		}
	}
	if len(f.Mounted) != 6 {
		t.Fatalf("want 6 mounts, got %+v", f.Mounted)
	}
	_, _ = s.Unmount(ctx, a)
	if len(f.Mounted) != 3 {
		t.Fatalf("unmounting A must leave B alone: %+v", f.Mounted)
	}
	for l, unc := range f.Mounted {
		if !strings.Contains(unc, "deck-b") {
			t.Errorf("%s still mapped to %s", l, unc)
		}
	}
}

func TestLetterConflictAcrossDevices(t *testing.T) {
	s, f := newSvc(t)
	a := addDevice(t, s, "Deck A", core.ModelDeckLCD, "deck-a", "pw")
	b := addDevice(t, s, "Deck B", core.ModelDeckOLED, "deck-b", "pw")
	b.Drives = a.Drives // force the same letters
	if _, err := s.Save(b, nil); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.Mount(ctx, a); err != nil {
		t.Fatal(err)
	}
	res, err := s.Mount(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.State != "failed" || !strings.Contains(r.Message, "already in use") {
			t.Errorf("expected conflict failure, got %+v", r)
		}
	}
	if len(f.Mounted) != 3 {
		t.Errorf("nothing of B should have mounted: %+v", f.Mounted)
	}
	st, _ := s.Status(ctx, b)
	if st[0].State != "conflict" {
		t.Errorf("status should say conflict, got %+v", st[0])
	}
	// And unmounting B must not remove A's drives.
	_, _ = s.Unmount(ctx, b)
	if len(f.Mounted) != 3 {
		t.Errorf("unmount of B removed A's drives: %+v", f.Mounted)
	}
}

func TestMountSkipsSDWhenNoCard(t *testing.T) {
	s, f := newSvc(t)
	d := addDevice(t, s, "Deck", core.ModelDeckLCD, "steamdeck", "pw")
	d.NoSD = true
	_, _ = s.Save(d, nil)
	res, err := s.Mount(context.Background(), d)
	if err != nil || len(res) != 2 {
		t.Fatalf("want 2 drives (X, Z), got %+v %v", res, err)
	}
	if _, ok := f.Mounted["Y"]; ok {
		t.Error("SD drive must not be mounted")
	}
}

func TestMountPrerequisites(t *testing.T) {
	s, f := newSvc(t)
	d := addDevice(t, s, "Deck", core.ModelDeckLCD, "steamdeck", "pw")
	ctx := context.Background()

	_ = s.Secrets.Delete(d.ID)
	var ve *ValidationError
	if _, err := s.Mount(ctx, d); !errors.As(err, &ve) {
		t.Errorf("missing password should be a ValidationError, got %v", err)
	}
	_ = s.Secrets.Set(d.ID, "pw")

	f.SSHFSOK = false
	if _, err := s.Mount(ctx, d); !errors.Is(err, ErrModulesMissing) {
		t.Errorf("want ErrModulesMissing, got %v", err)
	}
	f.SSHFSOK = true

	f.FailMount = errors.New("boom")
	res, err := s.Mount(ctx, d)
	if err != nil || res[0].State != "failed" || res[0].Message != "boom" {
		t.Errorf("drive failure should be reported per drive, got %+v %v", res, err)
	}
}

func TestExportUsesSavedPassword(t *testing.T) {
	s, _ := newSvc(t)
	d := addDevice(t, s, "Deck", core.ModelDeckLCD, "steamdeck", "hunter2")
	bat, err := s.Export(d)
	if err != nil || !strings.Contains(bat, `set "pass=hunter2"`) {
		t.Fatalf("export: %v\n%s", err, bat)
	}
}

func TestDuplicateAndDelete(t *testing.T) {
	s, _ := newSvc(t)
	d := addDevice(t, s, "Deck", core.ModelDeckLCD, "steamdeck", "pw")
	cp, err := s.Duplicate(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cp.Name != "Deck copy" || cp.ID == d.ID {
		t.Errorf("bad copy: %+v", cp)
	}
	if cp.Drives[0].Letter != "W" {
		t.Errorf("copy should move to free letters, got %+v", cp.Drives)
	}
	if _, have, _ := s.Secrets.Get(cp.ID); have {
		t.Error("password must not be copied")
	}
	cp2, _ := s.Duplicate(d.ID)
	if cp2.Name != "Deck copy 2" {
		t.Errorf("second copy name = %q", cp2.Name)
	}
	if err := s.Delete(d.ID); err != nil {
		t.Fatal(err)
	}
	if _, have, _ := s.Secrets.Get(d.ID); have {
		t.Error("password should be deleted with the device")
	}
}

func TestAutostartShortcutFollowsDevices(t *testing.T) {
	s, f := newSvc(t)
	a := addDevice(t, s, "Deck A", core.ModelDeckLCD, "deck-a", "pw")
	b := addDevice(t, s, "Deck B", core.ModelSteamMachine, "machine", "pw")
	last := func() string { // ignoring the clean-up of the shortcut's old name
		for i := len(f.Calls) - 1; i >= 0; i-- {
			if !strings.Contains(f.Calls[i], "sshfs-master autostart") {
				return f.Calls[i]
			}
		}
		return ""
	}
	before := len(f.Calls)
	a.Name = "Deck A2"
	_, _ = s.Save(a, nil)
	if len(f.Calls) != before { // nothing at all, not even the old-name clean-up
		t.Fatal("saving without changing AutoMount must not touch the shortcut")
	}
	a.AutoMount = true
	_, _ = s.Save(a, nil)
	if last() != "shortcut create Startup SSHFS Station autostart mount --auto" {
		t.Fatalf("expected startup shortcut, got %q", last())
	}
	b.AutoMount = true
	_, _ = s.Save(b, nil)
	a.AutoMount = false
	_, _ = s.Save(a, nil)
	if !strings.HasPrefix(last(), "shortcut create") {
		t.Fatalf("still one auto device, shortcut must stay: %q", last())
	}
	if err := s.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(last(), "shortcut remove Startup") {
		t.Fatalf("no auto devices left, shortcut must be removed: %q", last())
	}
}

func TestMountAuto(t *testing.T) {
	s, f := newSvc(t)
	a := addDevice(t, s, "Deck A", core.ModelDeckLCD, "deck-a", "pw")
	addDevice(t, s, "Deck B", core.ModelDeckOLED, "deck-b", "pw")
	a.AutoMount = true
	_, _ = s.Save(a, nil)
	res := s.MountAuto(context.Background())
	if len(res) != 1 || len(res["Deck A"]) != 3 || len(f.Mounted) != 3 {
		t.Fatalf("only Deck A should mount: %+v %+v", res, f.Mounted)
	}
}

func TestDesktopShortcut(t *testing.T) {
	s, f := newSvc(t)
	d := addDevice(t, s, "Deck: living/room", core.ModelDeckLCD, "deck-a", "pw")
	if _, err := s.DesktopShortcut(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	want := `shortcut create Desktop Mount Deck- living-room mount "Deck: living/room"`
	if got := f.Calls[len(f.Calls)-1]; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestStatusAll(t *testing.T) {
	s, _ := newSvc(t)
	a := addDevice(t, s, "Deck A", core.ModelDeckLCD, "deck-a", "pw")
	b := addDevice(t, s, "Deck B", core.ModelDeckOLED, "deck-b", "pw")
	_, _ = s.Mount(context.Background(), a)
	all, m, err := s.StatusAll(context.Background())
	if err != nil || len(all) != 2 {
		t.Fatal(err, all)
	}
	if all[a.ID][0].State != "mounted" || all[b.ID][0].State != "unmounted" {
		t.Errorf("status wrong: %+v", all)
	}
	if !contains(m.UsedLetters, "C") || !contains(m.UsedLetters, "X") {
		t.Errorf("used letters: %v", m.UsedLetters)
	}
}

func TestEditWhileMounted(t *testing.T) {
	s, f := newSvc(t)
	d := addDevice(t, s, "Deck", core.ModelDeckLCD, "steamdeck", "pw")
	ctx := context.Background()
	if _, err := s.Mount(ctx, d); err != nil {
		t.Fatal(err)
	}
	// Change the host and move Z: to W: while mounted.
	d.Host = "192.168.1.9"
	d.Drives[2].Letter = "W"
	if _, err := s.Save(d, nil); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Status(ctx, d)
	if len(st) != 4 {
		t.Fatalf("want X, Y (stale), W (unmounted) and old Z, got %+v", st)
	}
	if !st[0].Stale || st[0].State != "mounted" || !strings.Contains(st[0].Mapped, "steamdeck") {
		t.Errorf("X should be mounted with old settings: %+v", st[0])
	}
	if st[2].Letter != "W" || st[2].State != "unmounted" {
		t.Errorf("W should be unmounted: %+v", st[2])
	}
	if st[3].Letter != "Z" || st[3].Label != OldLabel {
		t.Errorf("old Z mapping should be listed: %+v", st[3])
	}

	// Mounting again replaces stale mappings with the new settings.
	res, _ := s.Mount(ctx, d, "X")
	if res[0].State != "mounted" || !strings.Contains(f.Mounted["X"], "192.168.1.9") {
		t.Fatalf("X should be remounted with the new host: %+v %+v", res, f.Mounted)
	}

	// Unmount all removes stale and old mappings too.
	if _, err := s.Unmount(ctx, d); err != nil {
		t.Fatal(err)
	}
	if len(f.Mounted) != 0 {
		t.Fatalf("everything should be unmounted: %+v", f.Mounted)
	}
}

func TestStaleNeverTouchesOtherDevices(t *testing.T) {
	s, f := newSvc(t)
	a := addDevice(t, s, "Deck A", core.ModelDeckLCD, "deck-a", "pw")
	b := addDevice(t, s, "Deck B", core.ModelDeckOLED, "deck-b", "pw")
	ctx := context.Background()
	_, _ = s.Mount(ctx, a)
	b.Drives = a.Drives // B now points at A's letters but never mounted them
	_, _ = s.Save(b, nil)
	if res, _ := s.Unmount(ctx, b); len(f.Mounted) != 3 {
		t.Fatalf("B must not unmount A's drives: %+v %+v", res, f.Mounted)
	}
	if res, _ := s.Mount(ctx, b); res[0].State != "failed" || len(f.Mounted) != 3 {
		t.Fatalf("B must not replace A's drives: %+v", res)
	}
}

func TestAutostartRemovesOldName(t *testing.T) {
	s, f := newSvc(t)
	d := addDevice(t, s, "Deck", core.ModelDeckLCD, "deck", "pw")
	d.AutoMount = true
	_, _ = s.Save(d, nil)
	if f.Calls[len(f.Calls)-1] != "shortcut remove Startup sshfs-master autostart " {
		t.Fatalf("old-name shortcut should be removed: %v", f.Calls)
	}
}
