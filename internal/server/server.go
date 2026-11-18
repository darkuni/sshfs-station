// Package server is the private HTTP API behind the app window. It listens on
// 127.0.0.1 only, checks the Host header (stops DNS rebinding) and requires a
// random per-run token on every API call (stops other web pages calling it).
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/darkuni/sshfs-station/internal/app"
	"github.com/darkuni/sshfs-station/internal/core"
)

// NewToken returns a random API token.
func NewToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Server serves the UI and the API.
type Server struct {
	Svc     *app.Service
	Token   string
	Host    string // expected Host header, e.g. 127.0.0.1:53111
	Static  fs.FS
	Version string

	mu       sync.Mutex
	lastPing time.Time
	pinged   bool
	bye      time.Time

	install installJob
}

type installJob struct {
	mu      sync.Mutex
	running bool
	done    bool
	log     []string
	err     string
}

// Alive reports whether the window still looks open. Before the first ping it
// allows startupGrace for the window to load.
func (s *Server) Alive(started time.Time, startupGrace, idle time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if !s.pinged {
		return now.Sub(started) < startupGrace
	}
	if !s.bye.IsZero() && s.bye.After(s.lastPing) && now.Sub(s.bye) > 4*time.Second {
		return false // the page said goodbye and did not come back (not a reload)
	}
	return now.Sub(s.lastPing) < idle
}

// Handler builds the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(s.Static)))

	api := func(pattern string, h func(http.ResponseWriter, *http.Request) (any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-SM-Token")), []byte(s.Token)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			v, err := h(w, r)
			writeResult(w, v, err)
		})
	}
	api("GET /api/info", s.info)
	api("POST /api/ping", s.ping)
	api("GET /api/devices", s.listDevices)
	api("POST /api/devices", s.createDevice)
	api("PUT /api/devices/{id}", s.saveDevice)
	api("DELETE /api/devices/{id}", s.deleteDevice)
	api("POST /api/devices/{id}/duplicate", s.duplicate)
	api("POST /api/devices/{id}/mount", s.mount)
	api("POST /api/devices/{id}/unmount", s.unmount)
	api("POST /api/devices/{id}/test", s.test)
	api("GET /api/devices/{id}/export", s.export)
	api("POST /api/devices/{id}/shortcut", s.shortcut)
	api("GET /api/status", s.status)
	api("GET /api/modules", s.modules)
	api("POST /api/modules/selftest", s.selftest)
	api("POST /api/modules/install", s.startInstall)
	api("GET /api/modules/install", s.installState)
	// sendBeacon cannot set headers, so the goodbye carries the token in the query.
	mux.HandleFunc("POST /api/bye", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("t")), []byte(s.Token)) == 1 {
			s.mu.Lock()
			s.bye = time.Now()
			s.mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Host != "" && r.Host != s.Host {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// YouTube refuses embedded videos when the page sends no referrer
		// (player "Error 153"), so use the browser's normal policy.
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	raw, err := fs.ReadFile(s.Static, "index.html")
	if err != nil {
		http.Error(w, "missing UI", http.StatusInternalServerError)
		return
	}
	page := strings.Replace(string(raw), "{{TOKEN}}", s.Token, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-src https://www.youtube-nocookie.com https://www.youtube.com; connect-src 'self'")
	io.WriteString(w, page)
}

// httpError carries a status code.
type httpError struct {
	code int
	msg  string
	body any
}

func (e *httpError) Error() string { return e.msg }

func badRequest(msg string) error { return &httpError{code: http.StatusBadRequest, msg: msg} }

func writeResult(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err == nil {
		if v == nil {
			v = map[string]bool{"ok": true}
		}
		json.NewEncoder(w).Encode(v)
		return
	}
	code := http.StatusInternalServerError
	body := map[string]any{"error": err.Error()}
	var he *httpError
	var ve *app.ValidationError
	switch {
	case errors.As(err, &he):
		code = he.code
		if he.body != nil {
			body["details"] = he.body
		}
	case errors.As(err, &ve):
		code = http.StatusUnprocessableEntity
		body["issues"] = ve.Issues
	case errors.Is(err, app.ErrModulesMissing):
		code = http.StatusPreconditionFailed
		body["code"] = "modules"
	case errors.Is(err, core.ErrNotFound):
		code = http.StatusNotFound
	}
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(body)
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return badRequest("bad request: " + err.Error())
	}
	return nil
}

// DeviceView is a device plus what the UI needs to show about it.
type DeviceView struct {
	core.Device
	HasPassword bool         `json:"hasPassword"`
	Issues      []core.Issue `json:"issues"`
}

func (s *Server) view(d core.Device) DeviceView {
	_, has, _ := s.Svc.Secrets.Get(d.ID)
	iss := s.Svc.Validate(d)
	if iss == nil {
		iss = []core.Issue{}
	}
	return DeviceView{Device: d, HasPassword: has, Issues: iss}
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) (any, error) {
	return map[string]any{
		"version":       s.Version,
		"demo":          s.Svc.Sys.Demo(),
		"models":        core.Models(),
		"maxDrives":     core.MaxDrives,
		"secureSecrets": s.Svc.Secrets.Secure(),
	}, nil
}

func (s *Server) ping(w http.ResponseWriter, r *http.Request) (any, error) {
	s.mu.Lock()
	s.lastPing, s.pinged = time.Now(), true
	s.mu.Unlock()
	return nil, nil
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) (any, error) {
	out := []DeviceView{}
	for _, d := range s.Svc.Store.List() {
		out = append(out, s.view(d))
	}
	return out, nil
}

func (s *Server) createDevice(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Model core.Model `json:"model"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	d := core.NewDevice(core.NewID(), req.Model, s.Svc.Store.List())
	if _, err := s.Svc.Save(d, nil); err != nil {
		return nil, err
	}
	return s.view(d), nil
}

func (s *Server) device(r *http.Request) (core.Device, error) {
	return s.Svc.Store.Get(r.PathValue("id"))
}

func (s *Server) saveDevice(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Device   core.Device `json:"device"`
		Password *string     `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	id := r.PathValue("id")
	if _, err := s.Svc.Store.Get(id); err != nil {
		return nil, err
	}
	req.Device.ID = id
	if len(req.Device.Drives) > core.MaxDrives {
		return nil, badRequest("too many drives")
	}
	if _, err := s.Svc.Save(req.Device, req.Password); err != nil {
		return nil, err
	}
	return s.view(req.Device), nil
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) (any, error) {
	return nil, s.Svc.Delete(r.PathValue("id"))
}

func (s *Server) duplicate(w http.ResponseWriter, r *http.Request) (any, error) {
	d, err := s.Svc.Duplicate(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	return s.view(d), nil
}

func letters(r *http.Request) ([]string, error) {
	var req struct {
		Letters []string `json:"letters"`
	}
	return req.Letters, decode(r, &req)
}

func (s *Server) mount(w http.ResponseWriter, r *http.Request) (any, error) {
	d, err := s.device(r)
	if err != nil {
		return nil, err
	}
	ls, err := letters(r)
	if err != nil {
		return nil, err
	}
	return s.Svc.Mount(r.Context(), d, ls...)
}

func (s *Server) unmount(w http.ResponseWriter, r *http.Request) (any, error) {
	d, err := s.device(r)
	if err != nil {
		return nil, err
	}
	ls, err := letters(r)
	if err != nil {
		return nil, err
	}
	return s.Svc.Unmount(r.Context(), d, ls...)
}

func (s *Server) test(w http.ResponseWriter, r *http.Request) (any, error) {
	d, err := s.device(r)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	return s.Svc.Test(ctx, d), nil
}

func (s *Server) export(w http.ResponseWriter, r *http.Request) (any, error) {
	d, err := s.device(r)
	if err != nil {
		return nil, err
	}
	bat, err := s.Svc.Export(d)
	if err != nil {
		return nil, err
	}
	return map[string]string{"bat": bat, "fileName": "mount-" + safeFile(d.Name) + ".bat"}, nil
}

func safeFile(n string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(n)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteByte('-')
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "device"
	}
	return out
}

func (s *Server) shortcut(w http.ResponseWriter, r *http.Request) (any, error) {
	d, err := s.device(r)
	if err != nil {
		return nil, err
	}
	if iss := s.Svc.Validate(d); core.HasErrors(iss) {
		return nil, &app.ValidationError{Issues: iss}
	}
	p, err := s.Svc.DesktopShortcut(r.Context(), d)
	if err != nil {
		return nil, err
	}
	return map[string]string{"path": p}, nil
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) (any, error) {
	all, m, err := s.Svc.StatusAll(r.Context())
	if err != nil {
		return nil, err
	}
	if m.UsedLetters == nil {
		m.UsedLetters = []string{}
	}
	return map[string]any{"devices": all, "usedLetters": m.UsedLetters, "sshfs": m.SSHFS}, nil
}

func (s *Server) modules(w http.ResponseWriter, r *http.Request) (any, error) {
	if r.URL.Query().Get("latest") == "1" {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		return s.Svc.Sys.ModulesWithLatest(ctx, r.URL.Query().Get("force") == "1"), nil
	}
	return s.Svc.Sys.Modules(r.Context()), nil
}

func (s *Server) selftest(w http.ResponseWriter, r *http.Request) (any, error) {
	return s.Svc.Sys.SelfTest(r.Context()), nil
}

func (s *Server) startInstall(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Update bool `json:"update"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	j := &s.install
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running {
		return nil, &httpError{code: http.StatusConflict, msg: "an install is already running"}
	}
	j.running, j.done, j.log, j.err = true, false, nil, ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		err := s.Svc.Sys.Install(ctx, req.Update, func(line string) {
			j.mu.Lock()
			j.log = append(j.log, line)
			j.mu.Unlock()
		})
		j.mu.Lock()
		j.running, j.done = false, true
		if err != nil {
			j.err = err.Error()
		}
		j.mu.Unlock()
	}()
	return nil, nil
}

func (s *Server) installState(w http.ResponseWriter, r *http.Request) (any, error) {
	j := &s.install
	j.mu.Lock()
	defer j.mu.Unlock()
	log := append([]string{}, j.log...)
	return map[string]any{"running": j.running, "done": j.done, "log": log, "error": j.err}, nil
}
