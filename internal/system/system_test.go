package system

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetectModules(t *testing.T) {
	pf, pf86 := t.TempDir(), t.TempDir()
	w, s := DetectModules(Roots{pf, pf86})
	if w.Installed || s.Installed {
		t.Fatal("nothing should be detected in empty folders")
	}
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(filepath.Join(pf86, "WinFsp", "bin", "winfsp-x64.dll"))
	w, s = DetectModules(Roots{pf, pf86})
	if !w.Installed || s.Installed {
		t.Fatalf("only WinFsp expected: %+v %+v", w, s)
	}
	mk(filepath.Join(pf, "SSHFS-Win", "bin", "sshfs-win.exe"))
	w, s = DetectModules(Roots{pf, pf86})
	if !w.Installed || !s.Installed || s.Path != filepath.Join(pf, "SSHFS-Win") {
		t.Fatalf("both expected: %+v %+v", w, s)
	}
}

func TestPickAsset(t *testing.T) {
	assets := []Asset{{Name: "winfsp-tests-2.0.zip"}, {Name: "winfsp-2.0.23075.msi"}, {Name: "sshfs-win-3.5.20357-x86.msi"}, {Name: "sshfs-win-3.5.20357-x64.msi"}}
	a, err := PickAsset("winfsp/winfsp", "amd64", assets)
	if err != nil || a.Name != "winfsp-2.0.23075.msi" {
		t.Errorf("winfsp: %v %v", a, err)
	}
	a, _ = PickAsset("winfsp/sshfs-win", "amd64", assets)
	if a.Name != "sshfs-win-3.5.20357-x64.msi" {
		t.Errorf("sshfs amd64: %v", a)
	}
	a, _ = PickAsset("winfsp/sshfs-win", "386", assets)
	if a.Name != "sshfs-win-3.5.20357-x86.msi" {
		t.Errorf("sshfs 386: %v", a)
	}
	if _, err := PickAsset("winfsp/winfsp", "amd64", []Asset{{Name: "readme.txt"}}); err == nil {
		t.Error("no msi should error")
	}
}

type fakeGitHub struct {
	srv        *httptest.Server
	badDigest  bool
	noDigest   bool
	payloads   map[string][]byte
	foreignURL bool
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	g := &fakeGitHub{payloads: map[string][]byte{
		"winfsp-2.0.msi":        []byte("winfsp-bytes"),
		"sshfs-win-3.5-x64.msi": []byte("sshfs-bytes"),
	}}
	mux := http.NewServeMux()
	release := func(repo, tag, file string) {
		mux.HandleFunc("/repos/"+repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
			body := g.payloads[file]
			sum := sha256.Sum256(body)
			digest := "sha256:" + hex.EncodeToString(sum[:])
			if g.badDigest {
				digest = "sha256:" + strings.Repeat("0", 64)
			}
			if g.noDigest {
				digest = ""
			}
			url := g.srv.URL + "/winfsp/dl/" + file
			if g.foreignURL {
				url = "https://evil.example/" + file
			}
			fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":%q,"browser_download_url":%q,"size":%d,"digest":%q}]}`, tag, file, url, len(body), digest)
		})
	}
	release("winfsp/winfsp", "v2.0", "winfsp-2.0.msi")
	release("winfsp/sshfs-win", "v3.5", "sshfs-win-3.5-x64.msi")
	mux.HandleFunc("/winfsp/dl/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(g.payloads[filepath.Base(r.URL.Path)])
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) installer(installed *[]string) *Installer {
	return &Installer{
		APIBase:        g.srv.URL,
		AllowURLPrefix: g.srv.URL + "/winfsp/",
		TempDir:        os.TempDir(),
		Arch:           "amd64",
		RunMSI: func(ctx context.Context, p string, log func(string)) error {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			*installed = append(*installed, filepath.Base(p)+"="+string(b))
			return nil
		},
	}
}

func TestInstallerHappyPathOrderAndSelection(t *testing.T) {
	g := newFakeGitHub(t)
	var got []string
	var log []string
	err := g.installer(&got).Install(context.Background(), true, true, func(s string) { log = append(log, s) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "winfsp-2.0.msi") || !strings.HasPrefix(got[1], "sshfs-win-3.5-x64.msi") {
		t.Fatalf("WinFsp must install first: %v", got)
	}
	if !strings.Contains(strings.Join(log, "\n"), "Checksum verified") {
		t.Errorf("expected checksum verified in log: %v", log)
	}
	got = nil
	if err := g.installer(&got).Install(context.Background(), false, true, func(string) {}); err != nil || len(got) != 1 {
		t.Fatalf("only SSHFS-Win requested: %v %v", got, err)
	}
}

func TestInstallerRejectsBadDownloads(t *testing.T) {
	var got []string
	g := newFakeGitHub(t)
	g.badDigest = true
	if err := g.installer(&got).Install(context.Background(), true, false, func(string) {}); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("bad digest must fail, got %v", err)
	}
	if len(got) != 0 {
		t.Error("nothing may be installed after a checksum failure")
	}
	g.badDigest, g.foreignURL = false, true
	if err := g.installer(&got).Install(context.Background(), true, false, func(string) {}); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("foreign URL must be refused, got %v", err)
	}
	g.foreignURL, g.noDigest = false, true
	var log []string
	if err := g.installer(&got).Install(context.Background(), true, false, func(s string) { log = append(log, s) }); err != nil {
		t.Errorf("missing digest should still install: %v", err)
	}
	if !strings.Contains(strings.Join(log, "\n"), "did not publish a checksum") {
		t.Errorf("should log the hash when no digest published: %v", log)
	}
}

func TestInstallerNeedsWindows(t *testing.T) {
	if err := (&Installer{}).Install(context.Background(), true, true, func(string) {}); err == nil {
		t.Error("no RunMSI should error")
	}
}

func sshServer(t *testing.T, banner string) (string, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if banner != "" {
					c.Write([]byte(banner))
				}
				time.Sleep(200 * time.Millisecond)
			}()
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

func TestTestConnection(t *testing.T) {
	ctx := context.Background()
	host, port := sshServer(t, "SSH-2.0-OpenSSH_9.9\r\n")
	steps := TestConnection(ctx, host, port)
	if len(steps) != 3 {
		t.Fatalf("want 3 steps, got %+v", steps)
	}
	for _, s := range steps {
		if !s.OK {
			t.Errorf("step failed: %+v", s)
		}
	}
	if !strings.Contains(steps[2].Detail, "OpenSSH_9.9") {
		t.Errorf("banner missing: %+v", steps[2])
	}

	host, port = sshServer(t, "HTTP/1.1 400 nope\r\n")
	steps = TestConnection(ctx, host, port)
	if last := steps[len(steps)-1]; last.OK || !strings.Contains(last.Detail, "does not look like SSH") {
		t.Errorf("non-SSH service should fail on the last step: %+v", steps)
	}

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closedPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	steps = TestConnection(ctx, "127.0.0.1", closedPort)
	if len(steps) != 2 || steps[1].OK || !strings.Contains(steps[1].Detail, "sshd") {
		t.Errorf("closed port: %+v", steps)
	}

	steps = TestConnection(ctx, "", 22)
	if len(steps) != 1 || steps[0].OK {
		t.Errorf("empty host: %+v", steps)
	}
	steps = TestConnection(ctx, "no-such-host.invalid", 22)
	if len(steps) != 1 || steps[0].OK {
		t.Errorf("unresolvable host: %+v", steps)
	}
}

func TestFriendlyNetUseError(t *testing.T) {
	cases := map[string]string{
		"System error 53 has occurred.\r\nThe network path was not found.": "could not find the device",
		"System error 1326 has occurred.":                                  "refused the username or password",
		"Systemfehler 86 aufgetreten.":                                     "refused the username or password",
		"System error 85 has occurred.":                                    "already in use",
		"System error 1219 has occurred.":                                  "different credentials",
		"System error 5 has occurred.\r\n\r\nAccess is denied.":            "sudo systemctl enable --now sshd",
		"Systemfehler 5 aufgetreten.":                                      "SSH is not running",
	}
	for in, want := range cases {
		if got := FriendlyNetUseError(in); !strings.Contains(got, want) {
			t.Errorf("FriendlyNetUseError(%q) = %q, want it to contain %q", in, got, want)
		}
	}
	if got := FriendlyNetUseError("System error 67 has occurred."); !strings.Contains(got, "use Flush DNS") {
		t.Errorf("error 67 should suggest Flush DNS: %q", got)
	}
	if got := FriendlyNetUseError("something odd"); got != "something odd" {
		t.Errorf("unknown output should pass through, got %q", got)
	}
	if FriendlyNetUseError("") == "" {
		t.Error("empty output needs a message")
	}
}

func TestParsePSDrives(t *testing.T) {
	out := "C|\r\nD|\r\nX|\\\\sshfs.r\\deck@steamdeck\\home\\deck\r\ny|\\\\SSHFS.R\\deck@steamdeck\\run\\media\\deck\\sd\r\nZ|\\\\nas\\share\r\n\r\ngarbage line\r\n"
	m := ParsePSDrives(out)
	if strings.Join(m.UsedLetters, "") != "CDXYZ" {
		t.Errorf("used letters = %v", m.UsedLetters)
	}
	if len(m.SSHFS) != 2 || m.SSHFS["X"] != `\\sshfs.r\deck@steamdeck\home\deck` || m.SSHFS["Y"] == "" {
		t.Errorf("sshfs mappings = %v", m.SSHFS)
	}
	if _, ok := m.SSHFS["Z"]; ok {
		t.Error("a non-SSHFS network drive is not an SSHFS mapping")
	}
}

func TestSameUNC(t *testing.T) {
	if !SameUNC(`\\SSHFS.R\Deck@SteamDeck\home\deck\`, `\\sshfs.r\deck@steamdeck\home\deck`) {
		t.Error("case and trailing slash should be ignored")
	}
	if SameUNC(`\\sshfs.r\deck@a`, `\\sshfs.r\deck@b`) || SameUNC("", `\\sshfs.r\deck@a`) {
		t.Error("different paths must not match")
	}
}

func TestFileSecrets(t *testing.T) {
	flip := func(b []byte) ([]byte, error) {
		o := make([]byte, len(b))
		for i := range b {
			o[i] = b[i] ^ 0x5a
		}
		return o, nil
	}
	s := &FileSecrets{Dir: filepath.Join(t.TempDir(), "sec"), Seal: flip, Open: flip, IsSecure: true}
	if _, ok, err := s.Get("abc"); ok || err != nil {
		t.Fatalf("missing secret: %v %v", ok, err)
	}
	if err := s.Set("abc", "p@ss w0rd"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.Dir, "abc.bin"))
	if strings.Contains(string(raw), "p@ss") {
		t.Error("password must not be stored in plain text")
	}
	if v, ok, err := s.Get("abc"); err != nil || !ok || v != "p@ss w0rd" {
		t.Fatalf("round trip: %q %v %v", v, ok, err)
	}
	if err := s.Set("abc", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get("abc"); ok {
		t.Error("empty password should delete the secret")
	}
	for _, bad := range []string{"", "../x", "a/b", `a\b`} {
		if err := s.Set(bad, "x"); err == nil {
			t.Errorf("id %q must be rejected", bad)
		}
	}
	_ = s.Delete("never-existed")
}
