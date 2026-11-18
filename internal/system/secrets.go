package system

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// FileSecrets keeps each password in its own file, sealed by Seal and opened
// by Open. On Windows Seal/Open use DPAPI, so the files can only be decrypted
// by the same Windows user on the same machine.
type FileSecrets struct {
	Dir      string
	Seal     func([]byte) ([]byte, error)
	Open     func([]byte) ([]byte, error)
	IsSecure bool
}

func (f *FileSecrets) path(id string) (string, error) {
	if !safeID.MatchString(id) {
		return "", errors.New("invalid device id")
	}
	return filepath.Join(f.Dir, id+".bin"), nil
}

func (f *FileSecrets) Set(id, password string) error {
	p, err := f.path(id)
	if err != nil {
		return err
	}
	if password == "" {
		return f.Delete(id)
	}
	sealed, err := f.Seal([]byte(password))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, sealed, 0o600)
}

func (f *FileSecrets) Get(id string) (string, bool, error) {
	p, err := f.path(id)
	if err != nil {
		return "", false, err
	}
	raw, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	plain, err := f.Open(raw)
	if err != nil {
		return "", false, errors.New("the saved password could not be decrypted (it was saved by a different Windows user or PC); enter it again")
	}
	return string(plain), true, nil
}

func (f *FileSecrets) Delete(id string) error {
	p, err := f.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (f *FileSecrets) Secure() bool { return f.IsSecure }
