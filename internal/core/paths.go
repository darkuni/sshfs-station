package core

import (
	"errors"
	"fmt"
	"strings"
)

// invalidPathChars cannot appear in a UNC path handed to Windows.
const invalidPathChars = "\"*?<>|"

func hasBadChars(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(invalidPathChars, r) {
			return true
		}
	}
	return false
}

// NormalizeCustomPath turns whatever a person typed ("home\deck\Games",
// "/mnt/stuff/") into a clean absolute POSIX path.
func NormalizeCustomPath(p string) (string, error) {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	if p == "" {
		return "", errors.New("custom path is empty")
	}
	if hasBadChars(p) {
		return "", fmt.Errorf("custom path contains characters that Windows cannot use: %s", invalidPathChars)
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p, nil
}

// RemotePath is the POSIX path a drive points at on the device.
func RemotePath(d Device, dr Drive) (string, error) {
	switch dr.Location {
	case LocHome:
		return "/home/" + trim(d.Username), nil
	case LocSD:
		if !d.HasSD() {
			return "", errors.New("no SD card name set")
		}
		return "/run/media/" + trim(d.Username) + "/" + trim(d.SDCard), nil
	case LocRoot:
		return "/", nil
	case LocCustom:
		return NormalizeCustomPath(dr.Custom)
	}
	return "", fmt.Errorf("unknown location %q", dr.Location)
}

// UNCPrefix is the SSHFS-Win share root for a device, for example
// \\sshfs.r\deck@steamdeck or \\sshfs.r\deck@192.168.1.5!2222.
// sshfs.r maps relative to the remote root, which is what the original tool used.
func UNCPrefix(d Device) string {
	s := `\\sshfs.r\` + trim(d.Username) + "@" + trim(d.Host)
	if p := d.EffectivePort(); p != 22 {
		s += "!" + itoa(p)
	}
	return s
}

// UNC returns the full network path for one drive.
func UNC(d Device, dr Drive) (string, error) {
	rp, err := RemotePath(d, dr)
	if err != nil {
		return "", err
	}
	if rp == "/" {
		return UNCPrefix(d), nil // root maps with no trailing path, as the original tool did
	}
	return UNCPrefix(d) + strings.ReplaceAll(rp, "/", `\`), nil
}

// LocationLabel is the human name for a drive's location.
func LocationLabel(l Location) string {
	switch l {
	case LocHome:
		return "Home"
	case LocSD:
		return "SD Card"
	case LocRoot:
		return "Root"
	case LocCustom:
		return "Custom"
	}
	return string(l)
}

// ActiveDrives are the drives that will actually be mapped. SD Card drives are
// skipped when the device has no SD card (or no card name entered yet).
func ActiveDrives(d Device) []Drive {
	var out []Drive
	for _, dr := range d.Drives {
		if dr.Location == LocSD && !d.HasSD() {
			continue
		}
		out = append(out, dr)
	}
	return out
}
