// SSHFS Station (sshfs-station) maps drives from Steam Decks, Steam Machines and Steam Frames
// into Windows using WinFsp and SSHFS-Win.
package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/darkuni/sshfs-station/internal/app"
	"github.com/darkuni/sshfs-station/internal/cli"
	"github.com/darkuni/sshfs-station/internal/core"
	"github.com/darkuni/sshfs-station/internal/launcher"
	"github.com/darkuni/sshfs-station/internal/server"
	"github.com/darkuni/sshfs-station/internal/system"
)

// version is set at build time with -ldflags "-X main.version=v1.0.0".
var version = "dev"

//go:embed web
var webFS embed.FS

type globals struct {
	demo     bool
	noWindow bool
	dataDir  string
	rest     []string
}

func parseGlobals(args []string) (globals, error) {
	var g globals
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--demo":
			g.demo = true
		case a == "--no-window":
			g.noWindow = true
		case a == "--data-dir":
			if i+1 >= len(args) {
				return g, errors.New("--data-dir needs a folder")
			}
			i++
			g.dataDir = args[i]
		case strings.HasPrefix(a, "--data-dir="):
			g.dataDir = strings.TrimPrefix(a, "--data-dir=")
		default:
			g.rest = append(g.rest, a)
		}
	}
	return g, nil
}

// defaultDataDir is %APPDATA%\sshfs-station, or a "data" folder next to the
// program when a file named "portable" sits beside it. Data from the app's
// earlier name (%APPDATA%\sshfs-master) is moved over the first time.
func defaultDataDir() (string, error) {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(dir, "portable")); err == nil {
			return filepath.Join(dir, "data"), nil
		}
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "sshfs-station")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		old := filepath.Join(base, "sshfs-master")
		if st, err := os.Stat(old); err == nil && st.IsDir() {
			_ = os.Rename(old, dir)
		}
	}
	return dir, nil
}

func main() {
	interactive := setupConsole()
	os.Exit(run(os.Args[1:], interactive))
}

func run(args []string, interactive bool) int {
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "sshfs-station:", err)
		if !interactive {
			messageBox(cli.Title, err.Error(), true)
		}
		return cli.Failed
	}
	g, err := parseGlobals(args)
	if err != nil {
		return fail(err)
	}
	if g.dataDir == "" {
		if g.dataDir, err = defaultDataDir(); err != nil {
			return fail(err)
		}
	}
	svc, err := newService(g)
	if err != nil {
		return fail(err)
	}

	if len(g.rest) > 0 {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		r := &cli.Runner{Svc: svc, Out: os.Stdout, Err: os.Stderr, Version: version,
			Interactive: interactive, Notify: messageBox}
		return r.Run(ctx, g.rest)
	}
	if err := runWindow(svc, g); err != nil {
		return fail(err)
	}
	return cli.OK
}

func newService(g globals) (*app.Service, error) {
	demo := g.demo || runtime.GOOS != "windows"
	var sys system.System
	var sec system.Secrets
	storePath := filepath.Join(g.dataDir, "devices.json")
	if demo {
		sys, sec = system.NewFake(), system.NewMemSecrets()
		storePath = filepath.Join(g.dataDir, "demo", "devices.json")
	} else {
		sys, sec = system.NewPlatform(g.dataDir)
	}
	st, err := core.OpenStore(storePath)
	if err != nil {
		return nil, err
	}
	svc := &app.Service{Store: st, Secrets: sec, Sys: sys}
	if demo {
		if err := seedDemo(svc); err != nil {
			return nil, err
		}
	}
	return svc, nil
}

// seedDemo fills demo mode with one of each Steam device so there is
// something to look at. Demo passwords live in memory only.
func seedDemo(svc *app.Service) error {
	if len(svc.Store.List()) == 0 {
		for _, s := range []struct {
			name  string
			model core.Model
			host  string
			sd    string
		}{
			{"Living room Deck", core.ModelDeckOLED, "steamdeck", "sdcard"},
			{"Travel Deck", core.ModelDeckLCD, "steamdeck-lcd", ""},
			{"Steam Machine", core.ModelSteamMachine, "steammachine", ""},
			{"Steam Frame", core.ModelSteamFrame, "steamframe", "sdcard"},
		} {
			d := core.NewDevice(core.NewID(), s.model, svc.Store.List())
			d.Name, d.Host, d.SDCard, d.NoSD = s.name, s.host, s.sd, s.sd == "" && s.model == core.ModelSteamMachine
			if err := svc.Store.Put(d); err != nil {
				return err
			}
		}
	}
	for _, d := range svc.Store.List() {
		if _, has, _ := svc.Secrets.Get(d.ID); !has {
			_ = svc.Secrets.Set(d.ID, "demo")
		}
	}
	return nil
}

func runWindow(svc *app.Service, g globals) error {
	instDir := g.dataDir
	if svc.Sys.Demo() {
		instDir = filepath.Join(g.dataDir, "demo")
	}
	if url, ok := launcher.Running(instDir); ok {
		_, err := launcher.Open(url, g.dataDir) // already running: just show it again
		return err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		return err
	}
	srv := &server.Server{Svc: svc, Token: server.NewToken(), Host: ln.Addr().String(), Static: static, Version: version}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go httpSrv.Serve(ln)

	url := "http://" + ln.Addr().String() + "/"
	if err := launcher.Register(instDir, url); err != nil {
		return err
	}
	defer launcher.Unregister(instDir)
	var window <-chan struct{}
	if !g.noWindow {
		if window, err = launcher.Open(url, g.dataDir); err != nil {
			return err
		}
	}
	fmt.Println("SSHFS Station is running at", url)

	// Stay up while the window is open. When the window's own browser process
	// can be watched, that decides; minimised windows are throttled by the
	// browser, so the page heartbeat is only a fallback with a long timeout.
	started := time.Now()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-sig:
			return shutdown(httpSrv)
		case <-window:
			if time.Since(started) > 5*time.Second {
				return shutdown(httpSrv) // the window was closed
			}
			window = nil // handed off to an already running browser: use the heartbeat
		case <-tick.C:
			idle := 3 * time.Minute
			if window != nil {
				idle = 365 * 24 * time.Hour // only the page's goodbye or the process exit end it
			}
			if !srv.Alive(started, 90*time.Second, idle) {
				return shutdown(httpSrv)
			}
		}
	}
}

func shutdown(s *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.Shutdown(ctx)
}
