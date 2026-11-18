package core

import (
	"path/filepath"
	"strings"
	"testing"
)

func deck() Device {
	d := NewDevice("a1", ModelDeckOLED, nil)
	d.Host = "steamdeck"
	d.SDCard = "sdcard"
	return d
}

func find(issues []Issue, field string, drive int, sev Severity) *Issue {
	for i := range issues {
		if issues[i].Field == field && issues[i].Drive == drive && issues[i].Severity == sev {
			return &issues[i]
		}
	}
	return nil
}

func TestNewDeviceDefaultsAndLetters(t *testing.T) {
	d1 := NewDevice("1", ModelDeckLCD, nil)
	got := []string{d1.Drives[0].Letter, d1.Drives[1].Letter, d1.Drives[2].Letter}
	if strings.Join(got, "") != "XYZ" {
		t.Fatalf("first device letters = %v, want X Y Z", got)
	}
	if d1.Drives[0].Location != LocHome || d1.Drives[1].Location != LocSD || d1.Drives[2].Location != LocRoot {
		t.Fatalf("first device locations wrong: %+v", d1.Drives)
	}
	if d1.Host != "steamdeck" || d1.Username != "deck" {
		t.Fatalf("deck defaults wrong: %q %q", d1.Host, d1.Username)
	}
	d2 := NewDevice("2", ModelDeckLCD, []Device{d1})
	if d2.Drives[0].Letter != "W" || d2.Drives[1].Letter != "V" || d2.Drives[2].Letter != "U" {
		t.Fatalf("second device letters = %+v", d2.Drives)
	}
	if d2.Name == d1.Name {
		t.Fatalf("names must be unique, both %q", d1.Name)
	}
	if d2.Name != "Steam Deck (LCD) 2" {
		t.Fatalf("unexpected unique name %q", d2.Name)
	}
}

func TestAllModelsHaveDefaults(t *testing.T) {
	for _, id := range []Model{ModelDeckLCD, ModelDeckOLED, ModelSteamMachine, ModelSteamFrame} {
		info, ok := ModelByID(id)
		if !ok || info.Label == "" {
			t.Fatalf("model %s missing", id)
		}
		d := NewDevice("x", id, nil)
		if d.Model != id || len(d.Drives) != 3 {
			t.Fatalf("model %s: bad new device %+v", id, d)
		}
	}
	if d := NewDevice("x", "bogus", nil); d.Model != ModelOther {
		t.Fatalf("unknown model should fall back to other, got %s", d.Model)
	}
}

func TestUNC(t *testing.T) {
	d := deck()
	d.Username = "deck"
	cases := []struct {
		dr   Drive
		want string
	}{
		{Drive{"X", LocHome, ""}, `\\sshfs.r\deck@steamdeck\home\deck`},
		{Drive{"Y", LocSD, ""}, `\\sshfs.r\deck@steamdeck\run\media\deck\sdcard`},
		{Drive{"Z", LocRoot, ""}, `\\sshfs.r\deck@steamdeck`},
		{Drive{"W", LocCustom, `home\deck\Games/`}, `\\sshfs.r\deck@steamdeck\home\deck\Games`},
	}
	for _, c := range cases {
		got, err := UNC(d, c.dr)
		if err != nil || got != c.want {
			t.Errorf("UNC(%v) = %q, %v; want %q", c.dr, got, err, c.want)
		}
	}
	d.Port = 2222
	if got, _ := UNC(d, Drive{"Z", LocRoot, ""}); got != `\\sshfs.r\deck@steamdeck!2222` {
		t.Errorf("port UNC = %q", got)
	}
	d.NoSD = true
	if _, err := UNC(d, Drive{"Y", LocSD, ""}); err == nil {
		t.Error("SD UNC should fail when there is no SD card")
	}
}

func TestNormalizeCustomPath(t *testing.T) {
	ok := map[string]string{"/a/b": "/a/b", "a\\b\\": "/a/b", "  //x///y/ ": "/x/y", "/": "/"}
	for in, want := range ok {
		got, err := NormalizeCustomPath(in)
		if err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "   ", "/a*b", "/a\"b", "/a|b"} {
		if _, err := NormalizeCustomPath(bad); err == nil {
			t.Errorf("Normalize(%q) should fail", bad)
		}
	}
}

func TestValidateHappyPath(t *testing.T) {
	if iss := Validate(deck(), nil, true); HasErrors(iss) {
		t.Fatalf("unexpected errors: %+v", iss)
	}
}

func TestValidateRequiredFields(t *testing.T) {
	d := deck()
	d.Name, d.Host, d.Username = "", "", ""
	iss := Validate(d, nil, false)
	for _, f := range []string{"name", "host", "username", "password"} {
		if find(iss, f, -1, Error) == nil {
			t.Errorf("expected %s error, got %+v", f, iss)
		}
	}
}

func TestValidateName(t *testing.T) {
	d := deck()
	d.Name = `My "Deck"`
	if find(Validate(d, nil, true), "name", -1, Error) == nil {
		t.Error("quote in name should error (it breaks command lines)")
	}
	d.Name = strings.Repeat("x", 61)
	if find(Validate(d, nil, true), "name", -1, Error) == nil {
		t.Error("over-long name should error")
	}
}

func TestValidateBadHostPortUser(t *testing.T) {
	d := deck()
	d.Host, d.Port, d.Username = "my deck!", 70000, "de ck"
	iss := Validate(d, nil, true)
	for _, f := range []string{"host", "port", "username"} {
		if find(iss, f, -1, Error) == nil {
			t.Errorf("expected %s error", f)
		}
	}
}

func TestValidateSDCard(t *testing.T) {
	d := deck()
	d.SDCard = ""
	iss := Validate(d, nil, true)
	if find(iss, "location", 1, Warn) == nil || HasErrors(iss) {
		t.Fatalf("blank SD should warn on the SD drive, not error: %+v", iss)
	}
	d.NoSD = true
	d.SDCard = "ignored/with/slashes"
	iss = Validate(d, nil, true)
	if HasErrors(iss) {
		t.Fatalf("a disabled SD field must not be validated: %+v", iss)
	}
	d.NoSD = false
	d.SDCard = "a/b"
	if find(Validate(d, nil, true), "sdCard", -1, Error) == nil {
		t.Fatal("slash in SD name should error")
	}
}

func TestValidateDrives(t *testing.T) {
	d := deck()
	d.Drives = []Drive{{"X", LocHome, ""}, {"X", LocRoot, ""}, {"C", LocRoot, ""}, {"Q", LocCustom, ""}, {"", LocHome, ""}}
	iss := Validate(d, nil, true)
	if find(iss, "letter", 1, Error) == nil {
		t.Error("duplicate letter not flagged on second drive")
	}
	if find(iss, "letter", 2, Error) == nil {
		t.Error("C: not flagged")
	}
	if find(iss, "custom", 3, Error) == nil {
		t.Error("empty custom path not flagged")
	}
	if find(iss, "letter", 4, Error) == nil {
		t.Error("blank letter not flagged")
	}
	d.Drives = nil
	if find(Validate(d, nil, true), "drives", -1, Error) == nil {
		t.Error("no drives should error")
	}
	d.Drives = []Drive{{"Y", LocSD, ""}}
	d.NoSD = true
	if find(Validate(d, nil, true), "drives", -1, Error) == nil {
		t.Error("only-SD drives with no SD card should error")
	}
}

func TestValidateAcrossDevices(t *testing.T) {
	a := NewDevice("a", ModelDeckLCD, nil)
	a.Host = "deck-a"
	b := NewDevice("b", ModelDeckOLED, nil)
	b.Host = "deck-b"
	b.Name = a.Name // clash
	b.Drives = a.Drives
	iss := Validate(b, []Device{a}, true)
	if find(iss, "name", -1, Error) == nil {
		t.Error("duplicate name across devices should error")
	}
	if find(iss, "letter", 0, Warn) == nil {
		t.Error("letter shared with another device should warn")
	}
	if HasErrors(Validate(a, []Device{a}, true)) {
		t.Error("a device must not clash with itself")
	}
}

func TestExportBatch(t *testing.T) {
	d := deck()
	d.Username = "deck"
	bat, err := ExportBatch(d, "p%ss&^")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"@echo off\r\n",
		`set "user=deck"`,
		`set "pass=p%%ss&^"`,
		`set "machine=steamdeck"`,
		"net use X: /delete /y",
		`net use X: "\\sshfs.r\%user%@%machine%\home\deck" /user:%user% "%pass%" /persistent:Yes`,
		`net use Y: "\\sshfs.r\%user%@%machine%\run\media\deck\sdcard" /user:%user% "%pass%" /persistent:Yes`,
		`net use Z: "\\sshfs.r\%user%@%machine%" /user:%user% "%pass%" /persistent:Yes`,
	} {
		if !strings.Contains(bat, want) {
			t.Errorf("bat missing %q\n%s", want, bat)
		}
	}
	if !strings.Contains(bat, "pause") {
		t.Error("pauses expected by default")
	}
	if strings.Contains(strings.ReplaceAll(bat, "\r\n", "\n"), "\r\r") || !strings.HasSuffix(bat, "\r\n") {
		t.Error("bad line endings")
	}
	d.NoPauses = true
	bat, _ = ExportBatch(d, "x")
	if strings.Contains(bat, "pause") {
		t.Error("no pauses requested but found pause")
	}
	d.NoSD = true
	bat, _ = ExportBatch(d, "x")
	if strings.Contains(bat, "Y:") {
		t.Error("SD drive must be left out when there is no SD card")
	}
	d.Port = 2222
	bat, _ = ExportBatch(d, "x")
	if !strings.Contains(bat, `\%user%@%machine%!2222\home\deck`) {
		t.Errorf("port missing from bat:\n%s", bat)
	}
}

func TestExportBatchRejects(t *testing.T) {
	d := deck()
	if _, err := ExportBatch(d, `bad"pass`); err == nil {
		t.Error("quote in password should be rejected")
	}
	if _, err := ExportBatch(d, ""); err == nil {
		t.Error("empty password should be rejected")
	}
	d.Host = ""
	if _, err := ExportBatch(d, "x"); err == nil {
		t.Error("invalid device should be rejected")
	}
}

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "devices.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 0 {
		t.Fatal("new store should be empty")
	}
	a := NewDevice(NewID(), ModelDeckLCD, nil)
	b := NewDevice(NewID(), ModelSteamMachine, []Device{a})
	b.Name = "aaa first"
	if err := s.Put(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(b); err != nil {
		t.Fatal(err)
	}
	a.Host = "changed"
	if err := s.Put(a); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	list := s2.List()
	if len(list) != 2 || list[0].Name != "aaa first" {
		t.Fatalf("reload/sort wrong: %+v", list)
	}
	got, err := s2.Get(a.ID)
	if err != nil || got.Host != "changed" {
		t.Fatalf("get after update: %+v %v", got, err)
	}
	if err := s2.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Get(a.ID); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := s2.Delete("nope"); err != ErrNotFound {
		t.Fatalf("delete missing: %v", err)
	}
}
