//go:build windows

package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const createNoWindow = 0x08000000

var (
	mpr                    = syscall.NewLazyDLL("mpr.dll")
	procWNetAddConnection2 = mpr.NewProc("WNetAddConnection2W")
	procWNetCancelConn2    = mpr.NewProc("WNetCancelConnection2W")
	procWNetGetConnection  = mpr.NewProc("WNetGetConnectionW")
	procGetLogicalDrives   = kernel32.NewProc("GetLogicalDrives")
)

const (
	resourceTypeDisk     = 1
	connectUpdateProfile = 0x1
	errMoreData          = 234
	errConnectionUnavail = 1201
)

type netResource struct {
	Scope, Type, DisplayType, Usage uint32
	LocalName, RemoteName           *uint16
	Comment, Provider               *uint16
}

// Windows is the real System. Drives are mapped through the Windows network
// API (the same thing `net use` calls), so the password never appears on a
// command line.
type Windows struct {
	inst   *Installer
	latest *LatestReleases

	verMu  sync.Mutex
	verAt  time.Time
	verWF  string
	verSSH string
}

// NewPlatform returns the Windows system and a DPAPI-backed secret store
// rooted in dataDir.
func NewPlatform(dataDir string) (System, Secrets) {
	inst := &Installer{Arch: runtime.GOARCH, RunMSI: runMSI}
	w := &Windows{inst: inst, latest: &LatestReleases{Inst: inst}}
	sec := &FileSecrets{
		Dir:      filepath.Join(dataDir, "secrets"),
		Seal:     dpapiProtect,
		Open:     dpapiUnprotect,
		IsSecure: true,
	}
	return w, sec
}

func (w *Windows) Demo() bool { return false }

func roots() Roots {
	return Roots{ProgramFiles: os.Getenv("ProgramFiles"), ProgramFilesX86: os.Getenv("ProgramFiles(x86)")}
}

// uninstallPS lists installed programs as "DisplayName|DisplayVersion".
const uninstallPS = `$k = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*', 'HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*'
Get-ItemProperty $k -ErrorAction SilentlyContinue | Where-Object { $_.DisplayName -like 'WinFsp*' -or $_.DisplayName -like 'SSHFS-Win*' } | ForEach-Object { $_.DisplayName + '|' + $_.DisplayVersion }`

// installedVersions reads versions from the Windows uninstall list, cached
// briefly because the window asks for module status regularly.
func (w *Windows) installedVersions(ctx context.Context, fresh bool) (string, string) {
	w.verMu.Lock()
	defer w.verMu.Unlock()
	if !fresh && !w.verAt.IsZero() && time.Since(w.verAt) < time.Minute {
		return w.verWF, w.verSSH
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := run(cctx, nil, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", uninstallPS)
	if err != nil {
		return w.verWF, w.verSSH
	}
	w.verWF, w.verSSH = ParseUninstall(out)
	w.verAt = time.Now()
	return w.verWF, w.verSSH
}

func (w *Windows) modules(ctx context.Context, fresh bool) ModuleStatus {
	wf, ss := DetectModules(roots())
	if wf.Installed || ss.Installed {
		vw, vs := w.installedVersions(ctx, fresh)
		if wf.Installed {
			wf.Version = vw
		}
		if ss.Installed {
			ss.Version = vs
		}
	}
	return ModuleStatus{Platform: "windows", Supported: true, WinFsp: wf, SSHFSWin: ss, Ready: wf.Installed && ss.Installed}
}

func (w *Windows) Modules(ctx context.Context) ModuleStatus { return w.modules(ctx, false) }

func (w *Windows) ModulesWithLatest(ctx context.Context, force bool) ModuleStatus {
	return WithLatest(ctx, w.modules(ctx, force), w.latest, force)
}

func (w *Windows) Install(ctx context.Context, update bool, log func(string)) error {
	ms := w.modules(ctx, true)
	if update {
		ms = WithLatest(ctx, ms, w.latest, true)
		if ms.LatestError != "" {
			return errors.New(ms.LatestError)
		}
	}
	needWF := !ms.WinFsp.Installed || ms.WinFsp.UpdateAvailable
	needSS := !ms.SSHFSWin.Installed || ms.SSHFSWin.UpdateAvailable
	if !needWF && !needSS {
		if update {
			log("WinFsp " + ms.WinFsp.Version + " and SSHFS-Win " + ms.SSHFSWin.Version + " are up to date.")
		} else {
			log("WinFsp and SSHFS-Win are already installed.")
		}
		return nil
	}
	if ms.WinFsp.UpdateAvailable {
		log("Updating WinFsp " + ms.WinFsp.Version + " to " + ms.WinFsp.Latest + ".")
	}
	if ms.SSHFSWin.UpdateAvailable {
		log("Updating SSHFS-Win " + ms.SSHFSWin.Version + " to " + ms.SSHFSWin.Latest + ".")
	}
	if err := w.inst.Install(ctx, needWF, needSS, log); err != nil {
		return err
	}
	after := w.modules(ctx, true)
	if !after.Ready {
		return errors.New("the installer finished but the components were not found afterwards; if Windows asked for a restart, restart and run the check again")
	}
	log("Now installed: WinFsp " + orStr(after.WinFsp.Version != "", after.WinFsp.Version, "(version unknown)") +
		", SSHFS-Win " + orStr(after.SSHFSWin.Version != "", after.SSHFSWin.Version, "(version unknown)") + ".")
	return nil
}

// run executes a command without a console window and returns its combined output.
func run(ctx context.Context, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func u16(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}

func netError(code uintptr) error {
	msg := fmt.Sprintf("System error %d has occurred.", code)
	if f := FriendlyNetUseError(msg); f != msg {
		return errors.New(f)
	}
	return fmt.Errorf("Windows error %d: %s", code, syscall.Errno(code).Error())
}

// callWithContext runs a blocking Windows call but stops waiting when ctx ends.
func callWithContext(ctx context.Context, f func() uintptr) (uintptr, error) {
	done := make(chan uintptr, 1)
	go func() { done <- f() }()
	select {
	case r := <-done:
		return r, nil
	case <-ctx.Done():
		return 0, errors.New("timed out waiting for the device; check that it is on and reachable")
	}
}

func (w *Windows) Mount(ctx context.Context, r MountRequest) error {
	nr := &netResource{Type: resourceTypeDisk, LocalName: u16(r.Letter + ":"), RemoteName: u16(r.UNC)}
	pw, user := u16(r.Password), u16(r.User)
	flags := uintptr(0)
	if r.Persistent {
		flags = connectUpdateProfile
	}
	rc, err := callWithContext(ctx, func() uintptr {
		rc, _, _ := procWNetAddConnection2.Call(uintptr(unsafe.Pointer(nr)), uintptr(unsafe.Pointer(pw)), uintptr(unsafe.Pointer(user)), flags)
		runtime.KeepAlive(nr)
		runtime.KeepAlive(pw)
		runtime.KeepAlive(user)
		return rc
	})
	if err != nil {
		return err
	}
	if rc != 0 {
		return netError(rc)
	}
	return nil
}

func (w *Windows) Unmount(ctx context.Context, letter string) error {
	name := u16(letter + ":")
	rc, err := callWithContext(ctx, func() uintptr {
		rc, _, _ := procWNetCancelConn2.Call(uintptr(unsafe.Pointer(name)), connectUpdateProfile, 1)
		runtime.KeepAlive(name)
		return rc
	})
	if err != nil {
		return err
	}
	if rc != 0 {
		return netError(rc)
	}
	return nil
}

func remoteNameOf(letter string) (string, bool) {
	local := u16(letter + ":")
	size := uint32(1024)
	buf := make([]uint16, size)
	rc, _, _ := procWNetGetConnection.Call(uintptr(unsafe.Pointer(local)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if rc == errMoreData && size > 0 && size < 1<<16 {
		buf = make([]uint16, size)
		rc, _, _ = procWNetGetConnection.Call(uintptr(unsafe.Pointer(local)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	}
	if rc == 0 || rc == errConnectionUnavail {
		return syscall.UTF16ToString(buf), true
	}
	return "", false
}

func (w *Windows) Mappings(ctx context.Context) (Mappings, error) {
	mask, _, _ := procGetLogicalDrives.Call()
	var b strings.Builder
	for i := 0; i < 26; i++ {
		l := string(rune('A' + i))
		remote, isNet := remoteNameOf(l)
		if mask&(1<<uint(i)) == 0 && !isNet {
			continue
		}
		b.WriteString(l + "|" + remote + "\n")
	}
	return ParsePSDrives(b.String()), nil
}

func (w *Windows) FlushDNS(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := run(cctx, nil, "ipconfig.exe", "/flushdns")
	if err != nil {
		return fmt.Errorf("Windows could not flush the DNS cache: %s", strings.TrimSpace(out))
	}
	return nil
}

func (w *Windows) SelfTest(ctx context.Context) []Step {
	wf, ss := DetectModules(roots())
	steps := []Step{
		{"WinFsp is installed", wf.Installed, orStr(wf.Installed, wf.Path, "Not found. Use Install on the Setup tab.")},
		{"SSHFS-Win is installed", ss.Installed, orStr(ss.Installed, ss.Path, "Not found. Use Install on the Setup tab.")},
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := run(cctx, nil, "sc.exe", "query", "WinFsp.Launcher")
	steps = append(steps, Step{"WinFsp launcher service is registered", err == nil,
		orStr(err == nil, "Registered.", "Not registered. Reinstall WinFsp (a restart may also be needed after installing).")})
	out, err := run(cctx, nil, "reg.exe", "query", `HKLM\SYSTEM\CurrentControlSet\Control\NetworkProvider\Order`, "/v", "ProviderOrder")
	np := err == nil && strings.Contains(strings.ToLower(out), "winfsp.np")
	steps = append(steps, Step{"WinFsp network provider is registered (\\\\sshfs paths work)", np,
		orStr(np, "Registered.", "Not registered. Restart Windows after installing WinFsp, or reinstall it.")})
	return steps
}

func orStr(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}

// runMSI installs an MSI elevated: PowerShell's Start-Process -Verb RunAs
// shows the standard Windows permission prompt. The path is passed through an
// environment variable so it never has to be quoted into the script.
func runMSI(ctx context.Context, msiPath string, log func(string)) error {
	script := `$ErrorActionPreference='Stop'; $p = Start-Process -FilePath msiexec.exe -ArgumentList @('/i', ('"' + $env:SM_MSI + '"'), '/passive', '/norestart') -Verb RunAs -Wait -PassThru; exit $p.ExitCode`
	out, err := run(ctx, []string{"SM_MSI=" + msiPath}, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && (ee.ExitCode() == 3010 || ee.ExitCode() == 1641) {
		log("Installed, but Windows says a restart is needed to finish.")
		return nil
	}
	if strings.Contains(strings.ToLower(out), "cancel") {
		return errors.New("administrator permission was declined")
	}
	return fmt.Errorf("the installer did not finish (%v) %s", err, strings.TrimSpace(out))
}

// shortcutPS creates a .lnk. All values come in through environment variables.
const shortcutPS = `$ErrorActionPreference='Stop'
$dir = [Environment]::GetFolderPath($env:SM_FOLDER)
$path = Join-Path $dir ($env:SM_FILE + '.lnk')
if ($env:SM_REMOVE -eq '1') { if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path }; Write-Output $path; exit 0 }
$s = (New-Object -ComObject WScript.Shell).CreateShortcut($path)
$s.TargetPath = $env:SM_EXE
$s.Arguments = $env:SM_ARGS
$s.WorkingDirectory = Split-Path -Parent $env:SM_EXE
$s.IconLocation = $env:SM_EXE + ',0'
$s.Description = $env:SM_DESC
$s.Save()
Write-Output $path`

func (w *Windows) Shortcut(ctx context.Context, sc Shortcut) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	remove := "0"
	if sc.Remove {
		remove = "1"
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := run(cctx, []string{
		"SM_FOLDER=" + string(sc.Folder), "SM_FILE=" + sc.FileName, "SM_EXE=" + exe,
		"SM_ARGS=" + sc.Args, "SM_DESC=" + sc.Description, "SM_REMOVE=" + remove,
	}, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", shortcutPS)
	if err != nil {
		return "", fmt.Errorf("could not create the shortcut: %s", strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), nil
}
