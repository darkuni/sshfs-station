package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ErrNotFound is returned when a device ID does not exist.
var ErrNotFound = errors.New("device not found")

// NewID returns a random device ID.
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type fileFormat struct {
	Version int      `json:"version"`
	Devices []Device `json:"devices"`
}

// Store keeps device profiles in a JSON file. It is safe for concurrent use.
// Passwords are never stored here.
type Store struct {
	mu      sync.Mutex
	path    string
	devices []Device
}

// OpenStore loads the store at path, creating it if it does not exist yet.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	var f fileFormat
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, errors.New("devices file is not valid JSON: " + err.Error())
	}
	s.devices = f.Devices
	return s, nil
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(fileFormat{Version: 1, Devices: s.devices}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns a copy of all devices, sorted by name.
func (s *Store) List() []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Device, len(s.devices))
	copy(out, s.devices)
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// Get returns one device by ID.
func (s *Store) Get(id string) (Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.ID == id {
			return d, nil
		}
	}
	return Device{}, ErrNotFound
}

// Put inserts or replaces a device (matched by ID) and saves to disk.
func (s *Store) Put(d Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.ID == "" {
		return errors.New("device has no ID")
	}
	replaced := false
	for i := range s.devices {
		if s.devices[i].ID == d.ID {
			s.devices[i] = d
			replaced = true
			break
		}
	}
	if !replaced {
		s.devices = append(s.devices, d)
	}
	return s.saveLocked()
}

// Delete removes a device by ID.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.devices {
		if s.devices[i].ID == id {
			s.devices = append(s.devices[:i], s.devices[i+1:]...)
			return s.saveLocked()
		}
	}
	return ErrNotFound
}
