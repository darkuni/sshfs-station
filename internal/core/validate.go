package core

import (
	"strings"
	"unicode"
)

// Severity of a validation issue.
type Severity string

const (
	Error Severity = "error"
	Warn  Severity = "warn"
)

// Issue is one problem found while validating a device. Field names match the
// UI: name, host, port, username, password, sdCard, drives, letter, location,
// custom. Drive is the index into Device.Drives, or -1 when not drive specific.
type Issue struct {
	Field    string   `json:"field"`
	Drive    int      `json:"drive"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
}

func isHostChar(r rune) bool {
	return r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_')
}

func allChars(s string, ok func(rune) bool) bool {
	for _, r := range s {
		if !ok(r) {
			return false
		}
	}
	return true
}

// Validate checks a device. others are the user's other devices (for name and
// drive-letter clashes). havePassword says whether a password is stored/entered.
// Errors block mounting and export; warnings do not.
func Validate(d Device, others []Device, havePassword bool) []Issue {
	var out []Issue
	add := func(field string, drive int, sev Severity, msg string) {
		out = append(out, Issue{field, drive, sev, msg})
	}

	if trim(d.Name) == "" {
		add("name", -1, Error, "Give this device a name so you can tell it apart from your others.")
	} else if strings.ContainsAny(d.Name, "\"\r\n") || len(trim(d.Name)) > 60 {
		add("name", -1, Error, "The name must be 60 characters or fewer and cannot contain double quotes.")
	} else {
		for _, o := range others {
			if o.ID != d.ID && strings.EqualFold(trim(o.Name), trim(d.Name)) {
				add("name", -1, Error, "Another device is already called \""+trim(d.Name)+"\". Names must be unique.")
				break
			}
		}
	}

	if trim(d.Host) == "" {
		add("host", -1, Error, "You must enter the name or IP address of your device. steamdeck is the default Deck name.")
	} else if !allChars(trim(d.Host), isHostChar) {
		add("host", -1, Error, "The device name or IP address can only contain letters, numbers, dots, dashes and underscores.")
	}

	if d.Port < 0 || d.Port > 65535 {
		add("port", -1, Error, "The SSH port must be between 1 and 65535 (leave it blank for 22).")
	}

	if trim(d.Username) == "" {
		add("username", -1, Error, "You must enter your username. deck is the default.")
	} else if !allChars(trim(d.Username), func(r rune) bool { return isHostChar(r) || r == '$' }) {
		add("username", -1, Error, "The username can only contain letters, numbers, dots, dashes and underscores.")
	}

	if !havePassword {
		add("password", -1, Error, "You must have a SUDO password on your device and it must be entered here.")
	}

	if !d.NoSD && trim(d.SDCard) != "" && (hasBadChars(d.SDCard) || strings.ContainsAny(d.SDCard, `/\`)) {
		add("sdCard", -1, Error, "The SD card name cannot contain slashes or characters Windows cannot use. Use only the folder name under run/media/"+orDefault(trim(d.Username), "deck")+".")
	}

	if len(d.Drives) == 0 {
		add("drives", -1, Error, "Add at least one drive mapping.")
	}
	if len(d.Drives) > MaxDrives {
		add("drives", -1, Error, "A device can have at most "+itoa(MaxDrives)+" drive mappings.")
	}

	seen := map[string]int{}
	active := 0
	for i, dr := range d.Drives {
		if len(dr.Letter) != 1 || dr.Letter[0] < 'A' || dr.Letter[0] > 'Z' {
			add("letter", i, Error, "Pick a drive letter from A to Z.")
			continue
		}
		if dr.Letter == "C" {
			add("letter", i, Error, "C: is your Windows system drive and cannot be used.")
			continue
		}
		if dr.Location == LocSD && !d.HasSD() {
			if d.NoSD {
				add("location", i, Warn, "Skipped: \"I don't have an SD card\" is checked.")
			} else {
				add("location", i, Warn, "Skipped until you fill in the SD Card Drive Name.")
			}
			continue
		}
		if dr.Location == LocCustom {
			if _, err := NormalizeCustomPath(dr.Custom); err != nil {
				add("custom", i, Error, "Custom location selected: "+err.Error()+".")
				continue
			}
		} else if dr.Location != LocHome && dr.Location != LocSD && dr.Location != LocRoot {
			add("location", i, Error, "Choose Home, SD Card, Root or Custom.")
			continue
		}
		active++
		if j, dup := seen[dr.Letter]; dup {
			add("letter", i, Error, "Letter "+dr.Letter+": is already used by Drive "+itoa(j+1)+". Pick a unique letter.")
		} else {
			seen[dr.Letter] = i
		}
		for _, o := range others {
			if o.ID == d.ID {
				continue
			}
			for _, od := range ActiveDrives(o) {
				if od.Letter == dr.Letter {
					add("letter", i, Warn, "Letter "+dr.Letter+": is also used by \""+o.Name+"\". Only one of them can be mounted at a time.")
				}
			}
		}
	}
	if len(d.Drives) > 0 && active == 0 {
		add("drives", -1, Error, "None of your drives can be mounted. Choose a location that does not need the SD card, or enter the SD Card Drive Name.")
	}
	return out
}

// HasErrors reports whether any issue is an error.
func HasErrors(issues []Issue) bool {
	for _, i := range issues {
		if i.Severity == Error {
			return true
		}
	}
	return false
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
