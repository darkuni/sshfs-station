package system

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Roots are the Program Files folders to look in.
type Roots struct{ ProgramFiles, ProgramFilesX86 string }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// DetectModules looks for WinFsp and SSHFS-Win on disk.
func DetectModules(r Roots) (winfsp, sshfs ModuleInfo) {
	winfsp = ModuleInfo{Name: "WinFsp"}
	for _, root := range []string{r.ProgramFilesX86, r.ProgramFiles} {
		if root == "" {
			continue
		}
		dir := filepath.Join(root, "WinFsp")
		if dlls, _ := filepath.Glob(filepath.Join(dir, "bin", "winfsp-*.dll")); len(dlls) > 0 {
			winfsp.Installed, winfsp.Path = true, dir
			break
		}
	}
	sshfs = ModuleInfo{Name: "SSHFS-Win"}
	for _, root := range []string{r.ProgramFiles, r.ProgramFilesX86} {
		if root == "" {
			continue
		}
		dir := filepath.Join(root, "SSHFS-Win")
		if exists(filepath.Join(dir, "bin", "sshfs-win.exe")) || exists(filepath.Join(dir, "bin", "sshfs.exe")) {
			sshfs.Installed, sshfs.Path = true, dir
			break
		}
	}
	return
}

// Asset is one downloadable file of a GitHub release.
type Asset struct {
	Name   string
	URL    string
	Size   int64
	Digest string // "sha256:<hex>" when GitHub publishes one
}

// Release is the part of a GitHub release the installer needs.
type Release struct {
	Tag    string
	URL    string // release page on GitHub
	Assets []Asset
}

// Installer downloads and installs WinFsp and SSHFS-Win from their official
// GitHub releases. Downloads are limited to github.com/winfsp/.
type Installer struct {
	Client         *http.Client
	APIBase        string // default https://api.github.com
	AllowURLPrefix string // default https://github.com/winfsp/
	TempDir        string
	Arch           string // runtime.GOARCH
	RunMSI         func(ctx context.Context, msiPath string, log func(string)) error
}

const (
	repoWinFsp  = "winfsp/winfsp"
	repoSSHFS   = "winfsp/sshfs-win"
	maxDownload = 128 << 20
)

func (in *Installer) client() *http.Client {
	if in.Client != nil {
		return in.Client
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func (in *Installer) apiBase() string {
	if in.APIBase != "" {
		return strings.TrimRight(in.APIBase, "/")
	}
	return "https://api.github.com"
}

func (in *Installer) allowPrefix() string {
	if in.AllowURLPrefix != "" {
		return in.AllowURLPrefix
	}
	return "https://github.com/winfsp/"
}

// Latest fetches the latest release of an owner/repo.
func (in *Installer) Latest(ctx context.Context, repo string) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.apiBase()+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "sshfs-station")
	resp, err := in.client().Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("could not reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("GitHub answered %s while looking up %s", resp.Status, repo)
	}
	var raw struct {
		Tag    string `json:"tag_name"`
		URL    string `json:"html_url"`
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Size   int64  `json:"size"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&raw); err != nil {
		return Release{}, err
	}
	rel := Release{Tag: raw.Tag, URL: raw.URL}
	for _, a := range raw.Assets {
		rel.Assets = append(rel.Assets, Asset{a.Name, a.URL, a.Size, a.Digest})
	}
	return rel, nil
}

// PickAsset chooses the MSI to install for a repo and CPU architecture.
func PickAsset(repo, arch string, assets []Asset) (Asset, error) {
	for _, a := range assets {
		n := strings.ToLower(a.Name)
		if !strings.HasSuffix(n, ".msi") {
			continue
		}
		switch repo {
		case repoWinFsp:
			if strings.HasPrefix(n, "winfsp-") {
				return a, nil
			}
		case repoSSHFS:
			want := "-x64.msi"
			if arch == "386" {
				want = "-x86.msi"
			}
			if strings.HasSuffix(n, want) {
				return a, nil
			}
		}
	}
	return Asset{}, fmt.Errorf("no suitable installer found in the latest %s release", repo)
}

// Download fetches an asset into dir, verifying its SHA-256 when GitHub
// publishes one, and returns the file path.
func (in *Installer) Download(ctx context.Context, a Asset, dir string, log func(string)) (string, error) {
	if !strings.HasPrefix(a.URL, in.allowPrefix()) {
		return "", fmt.Errorf("refusing to download from %s (only %s is allowed)", a.URL, in.allowPrefix())
	}
	if filepath.Base(a.Name) != a.Name || a.Name == "" {
		return "", errors.New("unsafe installer file name")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "sshfs-station")
	resp, err := in.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}
	path := filepath.Join(dir, a.Name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxDownload+1))
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return "", fmt.Errorf("download failed: %w", err)
	}
	if n > maxDownload {
		os.Remove(path)
		return "", errors.New("download is larger than expected")
	}
	if a.Size > 0 && n != a.Size {
		os.Remove(path)
		return "", fmt.Errorf("download is incomplete (%d of %d bytes)", n, a.Size)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if want, ok := strings.CutPrefix(strings.ToLower(a.Digest), "sha256:"); ok {
		if want != sum {
			os.Remove(path)
			return "", errors.New("checksum mismatch: the download does not match what GitHub published; it was deleted")
		}
		log("Checksum verified (SHA-256 " + sum[:16] + "...).")
	} else {
		log("SHA-256 of the download: " + sum + " (GitHub did not publish a checksum to compare against).")
	}
	return path, nil
}

// Install installs the requested modules, WinFsp first because SSHFS-Win needs it.
func (in *Installer) Install(ctx context.Context, winfsp, sshfs bool, log func(string)) error {
	if in.RunMSI == nil {
		return errors.New("installing is only supported on Windows")
	}
	dir, err := os.MkdirTemp(in.TempDir, "sshfs-station-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	type job struct {
		on          bool
		repo, label string
	}
	for _, j := range []job{{winfsp, repoWinFsp, "WinFsp"}, {sshfs, repoSSHFS, "SSHFS-Win"}} {
		if !j.on {
			continue
		}
		log("Looking up the latest " + j.label + " release ...")
		rel, err := in.Latest(ctx, j.repo)
		if err != nil {
			return err
		}
		a, err := PickAsset(j.repo, in.Arch, rel.Assets)
		if err != nil {
			return err
		}
		log("Downloading " + a.Name + " (" + rel.Tag + ") ...")
		path, err := in.Download(ctx, a, dir, log)
		if err != nil {
			return err
		}
		log("Installing " + j.label + " ... Windows will ask for administrator permission.")
		if err := in.RunMSI(ctx, path, log); err != nil {
			return fmt.Errorf("%s: %w", j.label, err)
		}
		log(j.label + " installed.")
	}
	return nil
}
