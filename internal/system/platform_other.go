//go:build !windows

package system

// NewPlatform returns the simulated demo system on non-Windows machines, so
// the app can still be run and developed anywhere. Nothing is really mounted.
func NewPlatform(dataDir string) (System, Secrets) {
	return NewFake(), NewMemSecrets()
}
