// Package cli is the command-line interface. It uses the same app.Service as
// the window, so a profile mounts exactly the same way from either.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/darkuni/sshfs-station/internal/app"
	"github.com/darkuni/sshfs-station/internal/core"
	"github.com/darkuni/sshfs-station/internal/system"
)

// Exit codes.
const (
	OK       = 0
	Failed   = 1
	BadUsage = 2
)

// Commands are the words that are never treated as a profile name.
var Commands = []string{"mount", "unmount", "status", "list", "test", "export", "check", "setup", "update", "help", "version"}

// IsCommand reports whether arg is a CLI command or help flag.
func IsCommand(arg string) bool {
	a := strings.ToLower(arg)
	if a == "-h" || a == "--help" || a == "/?" || a == "-v" || a == "--version" {
		return true
	}
	for _, c := range Commands {
		if a == c {
			return true
		}
	}
	return false
}

// Title is the product name shown in message boxes.
const Title = "SSHFS Station"

// Usage is the help text.
const Usage = `SSHFS Station - map Steam Deck, Steam Machine and Steam Frame drives in Windows

Usage:
  sshfs-station                          open the window
  sshfs-station "<profile>"              mount every drive of a profile (no window)
  sshfs-station mount "<profile>" [--letters X,Y] [--quiet]
  sshfs-station mount --auto             mount every profile set to mount at sign-in
  sshfs-station unmount "<profile>" [--letters X,Y]
  sshfs-station unmount --all            unmount every profile
  sshfs-station status ["<profile>"] [--json]
  sshfs-station list [--json]
  sshfs-station test "<profile>"         check the device can be reached over SSH
  sshfs-station export "<profile>" [--out file.bat]
  sshfs-station check                    check WinFsp and SSHFS-Win and their versions
  sshfs-station setup                    download and install whatever is missing
  sshfs-station update                   update WinFsp and SSHFS-Win to the latest releases
  sshfs-station help | version

Options for any command:
  --demo             use simulated drives and sample profiles (nothing is mounted)
  --data-dir <dir>   keep profiles somewhere else (for a portable copy)
  --no-window        start the app but do not open a window (prints its address)

Profile names are matched without caring about case, and a unique start of the
name is enough ("living" finds "Living room Deck"). Passwords are entered and
saved in the window. Exit code is 0 on success, 1 on failure, 2 on bad usage.
`

// Runner holds what a command needs.
type Runner struct {
	Svc     *app.Service
	Out     io.Writer
	Err     io.Writer
	Version string
	// Interactive is false when there is no console to print to (launched from a
	// shortcut). Notify is then used for a short summary.
	Interactive bool
	Notify      func(title, msg string, isError bool)
}

type opts struct {
	positional []string
	letters    []string
	json       bool
	quiet      bool
	auto       bool
	all        bool
	out        string
}

func parse(args []string) (opts, error) {
	var o opts
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			o.json = true
		case a == "--quiet" || a == "-q":
			o.quiet = true
		case a == "--auto":
			o.auto = true
		case a == "--all":
			o.all = true
		case a == "--letters" || a == "--out" || a == "-o":
			if i+1 >= len(args) {
				return o, fmt.Errorf("%s needs a value", a)
			}
			i++
			if a == "--letters" {
				for _, l := range strings.FieldsFunc(args[i], func(r rune) bool { return r == ',' || r == ' ' || r == ':' }) {
					o.letters = append(o.letters, strings.ToUpper(l))
				}
			} else {
				o.out = args[i]
			}
		case strings.HasPrefix(a, "--letters="):
			for _, l := range strings.FieldsFunc(strings.TrimPrefix(a, "--letters="), func(r rune) bool { return r == ',' || r == ' ' || r == ':' }) {
				o.letters = append(o.letters, strings.ToUpper(l))
			}
		case strings.HasPrefix(a, "--out="):
			o.out = strings.TrimPrefix(a, "--out=")
		case strings.HasPrefix(a, "--") && len(a) > 2:
			return o, fmt.Errorf("unknown option %s", a)
		default:
			o.positional = append(o.positional, a)
		}
	}
	return o, nil
}

func (r *Runner) printf(format string, a ...any) { fmt.Fprintf(r.Out, format, a...) }

// Run executes one command line (without the program name) and returns the exit code.
func (r *Runner) Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(r.Out, Usage)
		return OK
	}
	cmd := strings.ToLower(args[0])
	rest := args[1:]
	if !IsCommand(cmd) {
		cmd, rest = "mount", args // sshfs-station "Living room Deck"
	}
	o, err := parse(rest)
	if err != nil {
		return r.usage(err.Error())
	}
	switch cmd {
	case "help", "-h", "--help", "/?":
		fmt.Fprint(r.Out, Usage)
		return OK
	case "version", "-v", "--version":
		r.printf("sshfs-station %s\n", r.Version)
		return OK
	case "list":
		return r.list(o)
	case "status":
		return r.status(ctx, o)
	case "mount":
		if o.auto {
			return r.mountAuto(ctx)
		}
		return r.mount(ctx, o, true)
	case "unmount":
		return r.mount(ctx, o, false)
	case "test":
		return r.test(ctx, o)
	case "export":
		return r.export(o)
	case "check":
		return r.check(ctx)
	case "setup":
		return r.install(ctx, false)
	case "update":
		return r.install(ctx, true)
	}
	return r.usage("unknown command " + cmd)
}

func (r *Runner) usage(msg string) int {
	fmt.Fprintf(r.Err, "sshfs-station: %s\nRun \"sshfs-station help\" for usage.\n", msg)
	r.notify(Title, msg, true)
	return BadUsage
}

func (r *Runner) fail(err error) int {
	fmt.Fprintf(r.Err, "sshfs-station: %s\n", err)
	r.notify(Title, err.Error(), true)
	return Failed
}

func (r *Runner) notify(title, msg string, isErr bool) {
	if !r.Interactive && r.Notify != nil {
		r.Notify(title, msg, isErr)
	}
}

func (r *Runner) one(o opts) (core.Device, error) {
	switch len(o.positional) {
	case 0:
		return core.Device{}, errors.New("which profile? Give its name, for example: sshfs-station mount \"Living room Deck\"")
	case 1:
		return r.Svc.FindDevice(o.positional[0])
	}
	return core.Device{}, errors.New("too many names; put a profile name with spaces in double quotes")
}

func (r *Runner) list(o opts) int {
	devs := r.Svc.Store.List()
	if o.json {
		type row struct {
			Name, Model, Host string
			Drives            []string
		}
		var rows []row
		for _, d := range devs {
			var ls []string
			for _, dr := range core.ActiveDrives(d) {
				ls = append(ls, dr.Letter)
			}
			rows = append(rows, row{d.Name, string(d.Model), d.Host, ls})
		}
		return r.writeJSON(rows)
	}
	if len(devs) == 0 {
		r.printf("No profiles yet. Open SSHFS Station to add one.\n")
		return OK
	}
	for _, d := range devs {
		info, _ := core.ModelByID(d.Model)
		var ls []string
		for _, dr := range core.ActiveDrives(d) {
			ls = append(ls, dr.Letter+":")
		}
		r.printf("%-28s %-20s %-20s %s\n", d.Name, info.Label, d.Host, strings.Join(ls, " "))
	}
	return OK
}

func (r *Runner) writeJSON(v any) int {
	enc := json.NewEncoder(r.Out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return r.fail(err)
	}
	return OK
}

func (r *Runner) status(ctx context.Context, o opts) int {
	var devs []core.Device
	if len(o.positional) > 0 {
		d, err := r.one(o)
		if err != nil {
			return r.fail(err)
		}
		devs = []core.Device{d}
	} else {
		devs = r.Svc.Store.List()
	}
	all, _, err := r.Svc.StatusAll(ctx)
	if err != nil {
		return r.fail(err)
	}
	if o.json {
		out := map[string][]app.DriveResult{}
		for _, d := range devs {
			out[d.Name] = all[d.ID]
		}
		return r.writeJSON(out)
	}
	for _, d := range devs {
		r.printf("%s (%s)\n", d.Name, d.Host)
		r.printResults(all[d.ID])
	}
	return OK
}

func (r *Runner) printResults(res []app.DriveResult) {
	if len(res) == 0 {
		r.printf("  (no drives)\n")
	}
	for _, x := range res {
		line := fmt.Sprintf("  %s: %-8s %-10s %s", x.Letter, x.Label, x.State, x.UNC)
		if x.Message != "" && x.Message != "Already mounted." && x.Message != "Not mounted." {
			line += "\n       " + x.Message
		}
		r.printf("%s\n", line)
	}
}

func summarize(res []app.DriveResult) (ok, bad []string) {
	for _, x := range res {
		if x.State == "failed" {
			bad = append(bad, x.Letter+": "+x.Message)
		} else {
			ok = append(ok, x.Letter+":")
		}
	}
	return
}

func (r *Runner) mount(ctx context.Context, o opts, mount bool) int {
	verb, done := "Mounting", "mounted"
	if !mount {
		verb, done = "Unmounting", "unmounted"
	}
	if !mount && o.all {
		failed := false
		for _, d := range r.Svc.Store.List() {
			res, err := r.Svc.Unmount(ctx, d)
			if err != nil {
				fmt.Fprintf(r.Err, "%s: %s\n", d.Name, err)
				failed = true
				continue
			}
			r.printf("%s\n", d.Name)
			r.printResults(res)
			if _, bad := summarize(res); len(bad) > 0 {
				failed = true
			}
		}
		if failed {
			return Failed
		}
		return OK
	}
	d, err := r.one(o)
	if err != nil {
		return r.fail(err)
	}
	r.printf("%s %s (%s) ...\n", verb, d.Name, d.Host)
	var res []app.DriveResult
	if mount {
		res, err = r.Svc.Mount(ctx, d, o.letters...)
	} else {
		res, err = r.Svc.Unmount(ctx, d, o.letters...)
	}
	if err != nil {
		var ve *app.ValidationError
		if errors.As(err, &ve) {
			err = fmt.Errorf("profile \"%s\" needs fixing first: %s Open SSHFS Station to fix it", d.Name, ve.Error())
		}
		return r.fail(err)
	}
	if len(res) == 0 {
		return r.fail(fmt.Errorf("no matching drives in \"%s\"", d.Name))
	}
	r.printResults(res)
	ok, bad := summarize(res)
	if len(bad) > 0 {
		msg := fmt.Sprintf("%s: %d of %d drives failed.\n\n%s", d.Name, len(bad), len(res), strings.Join(bad, "\n"))
		r.notify(Title, msg, true)
		return Failed
	}
	if !o.quiet {
		r.notify(Title, fmt.Sprintf("%s: %s %s", d.Name, strings.Join(ok, " "), done), false)
	}
	return OK
}

func (r *Runner) mountAuto(ctx context.Context) int {
	// At sign-in the network can take a moment to come up; retry failures briefly.
	var res map[string][]app.DriveResult
	for attempt := 0; attempt < 4; attempt++ {
		res = r.Svc.MountAuto(ctx)
		retry := false
		for _, rs := range res {
			if _, bad := summarize(rs); len(bad) > 0 {
				retry = true
			}
		}
		if !retry {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
	names := make([]string, 0, len(res))
	for n := range res {
		names = append(names, n)
	}
	sort.Strings(names)
	var problems []string
	for _, n := range names {
		r.printf("%s\n", n)
		r.printResults(res[n])
		if _, bad := summarize(res[n]); len(bad) > 0 {
			problems = append(problems, n+":\n  "+strings.Join(bad, "\n  "))
		}
	}
	if len(names) == 0 {
		r.printf("No profiles are set to mount at sign-in.\n")
	}
	if len(problems) > 0 {
		r.notify(Title, "Some drives could not be mounted at sign-in.\n\n"+strings.Join(problems, "\n"), true)
		return Failed
	}
	return OK
}

func (r *Runner) test(ctx context.Context, o opts) int {
	d, err := r.one(o)
	if err != nil {
		return r.fail(err)
	}
	r.printf("Testing %s (%s, port %d)\n", d.Name, d.Host, d.EffectivePort())
	failed := false
	for _, s := range r.Svc.Test(ctx, d) {
		mark := "OK  "
		if !s.OK {
			mark, failed = "FAIL", true
		}
		r.printf("  [%s] %s: %s\n", mark, s.Name, s.Detail)
	}
	if failed {
		r.notify(Title, d.Name+": connection test failed. Run sshfs-station test from a terminal for details.", true)
		return Failed
	}
	r.notify(Title, d.Name+": connection test passed.", false)
	return OK
}

func (r *Runner) export(o opts) int {
	d, err := r.one(o)
	if err != nil {
		return r.fail(err)
	}
	bat, err := r.Svc.Export(d)
	if err != nil {
		return r.fail(err)
	}
	if o.out == "" {
		fmt.Fprint(r.Out, bat)
		return OK
	}
	if err := os.WriteFile(o.out, []byte(bat), 0o600); err != nil {
		return r.fail(err)
	}
	r.printf("Wrote %s. It contains the password in plain text, so keep it private.\n", o.out)
	return OK
}

func versionLine(m system.ModuleInfo) string {
	if !m.Installed {
		return "not installed"
	}
	v := m.Version
	if v == "" {
		v = "installed (version unknown)"
	}
	switch {
	case m.UpdateAvailable:
		v += ", update available: " + m.Latest
	case m.Latest != "" && m.Version != "":
		v += ", up to date"
	}
	return v
}

func (r *Runner) check(ctx context.Context) int {
	lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	ms := r.Svc.Sys.ModulesWithLatest(lctx, false)
	cancel()
	r.printf("  WinFsp:    %s\n", versionLine(ms.WinFsp))
	r.printf("  SSHFS-Win: %s\n", versionLine(ms.SSHFSWin))
	if ms.LatestError != "" {
		r.printf("  (%s)\n", ms.LatestError)
	}
	r.printf("\n")
	failed := false
	for _, s := range r.Svc.Sys.SelfTest(ctx) {
		mark := "OK  "
		if !s.OK {
			mark, failed = "FAIL", true
		}
		r.printf("  [%s] %s: %s\n", mark, s.Name, s.Detail)
	}
	if !ms.Ready {
		r.printf("\nRun \"sshfs-station setup\" to install what is missing.\n")
	} else if ms.UpdatesAvailable {
		r.printf("\nRun \"sshfs-station update\" to install the updates. Unmount your drives first.\n")
	}
	if failed {
		return Failed
	}
	return OK
}

func (r *Runner) install(ctx context.Context, update bool) int {
	if err := r.Svc.Sys.Install(ctx, update, func(s string) { r.printf("%s\n", s) }); err != nil {
		return r.fail(err)
	}
	return r.check(ctx)
}
