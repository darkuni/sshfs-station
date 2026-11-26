package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/darkuni/sshfs-station/internal/app"
	"github.com/darkuni/sshfs-station/internal/core"
	"github.com/darkuni/sshfs-station/internal/system"
)

type env struct {
	t    *testing.T
	srv  *Server
	ts   *httptest.Server
	fake *system.Fake
}

func newEnv(t *testing.T) *env {
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "d.json"))
	if err != nil {
		t.Fatal(err)
	}
	f := system.NewFake()
	s := &Server{
		Svc:    &app.Service{Store: st, Secrets: system.NewMemSecrets(), Sys: f},
		Token:  "tok",
		Static: fstest.MapFS{"index.html": {Data: []byte(`<meta name="t" content="{{TOKEN}}">`)}, "app.js": {Data: []byte("js")}},
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	s.Host = strings.TrimPrefix(ts.URL, "http://")
	return &env{t, s, ts, f}
}

func (e *env) call(method, path string, body any, out any) int {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	req.Header.Set("X-SM-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		raw, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatalf("%s %s: bad json %v: %s", method, path, err, raw)
		}
	}
	return resp.StatusCode
}

func TestSecurity(t *testing.T) {
	e := newEnv(t)
	resp, _ := http.Get(e.ts.URL + "/api/devices")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("no token must be forbidden, got %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/devices", nil)
	req.Header.Set("X-SM-Token", "tok")
	req.Host = "evil.example:80"
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("wrong Host must be forbidden, got %d", resp.StatusCode)
	}
	resp, _ = http.Get(e.ts.URL + "/")
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `content="tok"`) || resp.Header.Get("Content-Security-Policy") == "" {
		t.Errorf("index should carry token and CSP: %s", b)
	}
	resp, _ = http.Get(e.ts.URL + "/assets/app.js")
	if resp.StatusCode != 200 {
		t.Errorf("static asset: %d", resp.StatusCode)
	}
}

func TestDeviceLifecycle(t *testing.T) {
	e := newEnv(t)
	var d DeviceView
	if c := e.call("POST", "/api/devices", map[string]string{"model": "steam-frame"}, &d); c != 200 {
		t.Fatalf("create: %d", c)
	}
	if d.Name != "Steam Frame" || d.HasPassword || len(d.Drives) != 3 {
		t.Fatalf("new device: %+v", d)
	}
	hasIssue := func(v DeviceView, field string) bool {
		for _, i := range v.Issues {
			if i.Field == field && i.Severity == core.Error {
				return true
			}
		}
		return false
	}
	if !hasIssue(d, "host") || !hasIssue(d, "password") {
		t.Errorf("new Frame needs host and password: %+v", d.Issues)
	}

	d.Host, d.SDCard = "frame.local", "card"
	pw := "secret"
	var saved DeviceView
	if c := e.call("PUT", "/api/devices/"+d.ID, map[string]any{"device": d.Device, "password": pw}, &saved); c != 200 {
		t.Fatalf("save: %d", c)
	}
	if !saved.HasPassword || core.HasErrors(saved.Issues) {
		t.Fatalf("saved: %+v", saved)
	}
	// Saving again without a password keeps it.
	e.call("PUT", "/api/devices/"+d.ID, map[string]any{"device": d.Device}, &saved)
	if !saved.HasPassword {
		t.Error("omitting password must keep the saved one")
	}

	var res []app.DriveResult
	if c := e.call("POST", "/api/devices/"+d.ID+"/mount", map[string]any{}, &res); c != 200 || len(res) != 3 || len(e.fake.Mounted) != 3 {
		t.Fatalf("mount: %d %+v", c, res)
	}
	var st struct {
		Devices     map[string][]app.DriveResult
		UsedLetters []string
	}
	e.call("GET", "/api/status", nil, &st)
	if st.Devices[d.ID][0].State != "mounted" {
		t.Errorf("status: %+v", st)
	}
	e.call("POST", "/api/devices/"+d.ID+"/unmount", map[string]any{"letters": []string{"X"}}, &res)
	if len(e.fake.Mounted) != 2 {
		t.Errorf("unmount X only: %+v", e.fake.Mounted)
	}

	var exp map[string]string
	if c := e.call("GET", "/api/devices/"+d.ID+"/export", nil, &exp); c != 200 || !strings.Contains(exp["bat"], "frame.local") || exp["fileName"] != "mount-steam-frame.bat" {
		t.Errorf("export: %d %+v", c, exp)
	}

	var sc map[string]string
	if c := e.call("POST", "/api/devices/"+d.ID+"/shortcut", nil, &sc); c != 200 || !strings.Contains(sc["path"], "Mount Steam Frame") {
		t.Errorf("shortcut: %d %+v", c, sc)
	}

	var cp DeviceView
	e.call("POST", "/api/devices/"+d.ID+"/duplicate", nil, &cp)
	if cp.Name != "Steam Frame copy" || cp.HasPassword {
		t.Errorf("duplicate: %+v", cp)
	}
	var list []DeviceView
	e.call("GET", "/api/devices", nil, &list)
	if len(list) != 2 {
		t.Errorf("list: %d", len(list))
	}
	if c := e.call("DELETE", "/api/devices/"+cp.ID, nil, nil); c != 200 {
		t.Errorf("delete: %d", c)
	}
	if c := e.call("DELETE", "/api/devices/"+cp.ID, nil, nil); c != 404 {
		t.Errorf("delete again should 404, got %d", c)
	}
}

func TestErrorsMapToStatusCodes(t *testing.T) {
	e := newEnv(t)
	var d DeviceView
	e.call("POST", "/api/devices", map[string]string{"model": "deck-lcd"}, &d)
	var body map[string]any
	if c := e.call("POST", "/api/devices/"+d.ID+"/mount", nil, &body); c != 422 || body["issues"] == nil {
		t.Errorf("invalid device mount should be 422 with issues: %d %v", c, body)
	}
	pw := "pw"
	d.SDCard = "x"
	e.call("PUT", "/api/devices/"+d.ID, map[string]any{"device": d.Device, "password": pw}, nil)
	e.fake.WinFspOK = false
	if c := e.call("POST", "/api/devices/"+d.ID+"/mount", nil, &body); c != 412 || body["code"] != "modules" {
		t.Errorf("modules missing should be 412: %d %v", c, body)
	}
	if c := e.call("PUT", "/api/devices/nope", map[string]any{"device": d.Device}, nil); c != 404 {
		t.Errorf("unknown id should 404, got %d", c)
	}
}

func TestInstallJob(t *testing.T) {
	e := newEnv(t)
	e.fake.WinFspOK, e.fake.SSHFSOK = false, false
	if c := e.call("POST", "/api/modules/install", nil, nil); c != 200 {
		t.Fatalf("start: %d", c)
	}
	var st struct {
		Running, Done bool
		Log           []string
		Error         string
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		e.call("GET", "/api/modules/install", nil, &st)
		if st.Done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !st.Done || st.Error != "" || len(st.Log) == 0 {
		t.Fatalf("install state: %+v", st)
	}
	var ms system.ModuleStatus
	e.call("GET", "/api/modules", nil, &ms)
	if !ms.Ready {
		t.Error("modules should be ready after install")
	}
}

func TestAlive(t *testing.T) {
	e := newEnv(t)
	start := time.Now()
	if !e.srv.Alive(start, time.Minute, time.Minute) {
		t.Error("should be alive during startup grace")
	}
	if e.srv.Alive(start.Add(-2*time.Minute), time.Minute, time.Minute) {
		t.Error("never pinged after grace should be dead")
	}
	e.call("POST", "/api/ping", nil, nil)
	if !e.srv.Alive(start, 0, time.Minute) {
		t.Error("recent ping should be alive")
	}
	if e.srv.Alive(start, 0, 0) {
		t.Error("stale ping should be dead")
	}
	http.Post(e.ts.URL+"/api/bye?t=tok", "text/plain", nil)
	e.srv.mu.Lock()
	e.srv.bye = time.Now().Add(-5 * time.Second)
	e.srv.lastPing = e.srv.bye.Add(-time.Second)
	e.srv.mu.Unlock()
	if e.srv.Alive(start, 0, time.Hour) {
		t.Error("goodbye without a later ping should be dead")
	}
}

func TestModulesLatestAndUpdate(t *testing.T) {
	e := newEnv(t)
	var ms system.ModuleStatus
	e.call("GET", "/api/modules", nil, &ms)
	if ms.Checked || ms.WinFsp.Version != "2.0.23075" {
		t.Errorf("plain status must not go online: %+v", ms)
	}
	e.call("GET", "/api/modules?latest=1", nil, &ms)
	if !ms.Checked || !ms.WinFsp.UpdateAvailable {
		t.Errorf("latest: %+v", ms)
	}
	if c := e.call("POST", "/api/modules/install", map[string]bool{"update": true}, nil); c != 200 {
		t.Fatalf("start update: %d", c)
	}
	var st struct{ Done bool }
	for i := 0; i < 100 && !st.Done; i++ {
		time.Sleep(20 * time.Millisecond)
		e.call("GET", "/api/modules/install", nil, &st)
	}
	e.call("GET", "/api/modules?latest=1", nil, &ms)
	if ms.UpdatesAvailable || ms.WinFsp.Version != "2.1.25156" {
		t.Errorf("after update: %+v", ms)
	}
}

func TestFlushDNSEndpoint(t *testing.T) {
	e := newEnv(t)
	if c := e.call("POST", "/api/dns/flush", nil, nil); c != 200 {
		t.Fatalf("flush: %d", c)
	}
	if e.fake.Calls[len(e.fake.Calls)-1] != "flushdns" {
		t.Errorf("not called: %v", e.fake.Calls)
	}
}
