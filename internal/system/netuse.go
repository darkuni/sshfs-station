package system

import (
	"regexp"
	"sort"
	"strings"
)

// The first number on the first line of `net use` output is the Windows error
// code ("System error 53 has occurred." / "Systemfehler 53 aufgetreten.").
var firstNumRe = regexp.MustCompile(`\b(\d{1,5})\b`)

func errorCode(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if m := firstNumRe.FindStringSubmatch(line); m != nil {
			return m[1]
		}
		return ""
	}
	return ""
}

// SSHDHint is shown for Windows error 5 (access denied), which SSHFS-Win
// returns when nothing on the device accepts the SSH connection.
const SSHDHint = "Windows was denied access (error 5). This usually means SSH is not running on the device. " +
	"On the device, switch to Desktop Mode, open Konsole and run: sudo systemctl enable --now sshd. Then try Test connection."

// FlushDNSHint is shown when Windows cannot find the device (errors 53 and 67).
const FlushDNSHint = "Windows could not find the device or SSHFS-Win. Check that the device is on, the name or IP is right, and that WinFsp and SSHFS-Win are installed (see the Setup tab). " +
	"If the device's IP address changed, use Flush DNS and try again."

// FriendlyNetUseError turns the output of a failed `net use` into advice a
// person can act on. The numeric Windows error code survives localisation, so
// the code is matched rather than the wording.
func FriendlyNetUseError(output string) string {
	output = strings.TrimSpace(output)
	if code := errorCode(output); code != "" {
		switch code {
		case "5":
			return SSHDHint
		case "53", "67":
			return FlushDNSHint
		case "86", "1326":
			return "The device refused the username or password. Check the SUDO password and the username."
		case "85":
			return "That drive letter is already in use. Pick another letter."
		case "1219":
			return "Windows already has a connection to this device with different credentials. Unmount its other drives first, then try again."
		case "1223":
			return "The connection was cancelled."
		case "2250":
			return "That drive letter is not mapped."
		}
	}
	if output == "" {
		return "Windows reported an unknown error."
	}
	return output
}

// ParsePSDrives parses lines of "Letter|DisplayRoot" produced by listing file
// system drives. It returns the SSHFS-Win mappings and every letter in use.
func ParsePSDrives(out string) Mappings {
	m := Mappings{SSHFS: map[string]string{}}
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		letter, root, ok := strings.Cut(line, "|")
		letter = strings.ToUpper(strings.TrimSpace(letter))
		if !ok || len(letter) != 1 || letter[0] < 'A' || letter[0] > 'Z' {
			continue
		}
		if !seen[letter] {
			seen[letter] = true
			m.UsedLetters = append(m.UsedLetters, letter)
		}
		root = strings.TrimSpace(root)
		if strings.HasPrefix(strings.ToLower(root), `\\sshfs`) {
			m.SSHFS[letter] = root
		}
	}
	sort.Strings(m.UsedLetters)
	return m
}

// SameUNC compares two UNC paths the way Windows does: ignoring case and a
// trailing backslash.
func SameUNC(a, b string) bool {
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), `\`)) }
	return norm(a) == norm(b)
}
