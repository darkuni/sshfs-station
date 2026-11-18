//go:build windows

package system

import (
	"errors"
	"syscall"
	"unsafe"
)

var (
	crypt32       = syscall.NewLazyDLL("crypt32.dll")
	kernel32      = syscall.NewLazyDLL("kernel32.dll")
	procProtect   = crypt32.NewProc("CryptProtectData")
	procUnprotect = crypt32.NewProc("CryptUnprotectData")
	procLocalFree = kernel32.NewProc("LocalFree")
)

const cryptProtectUIForbidden = 0x1

type dataBlob struct {
	cbData uint32
	pbData *byte
}

// The salt keeps its original value so passwords saved before the rename still open.
var entropy = []byte("sshfs-master")

func newBlob(b []byte) *dataBlob {
	if len(b) == 0 {
		return &dataBlob{}
	}
	return &dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

func (b *dataBlob) bytes() []byte {
	if b.cbData == 0 || b.pbData == nil {
		return nil
	}
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	return out
}

// dpapiProtect encrypts data so only the current Windows user can decrypt it.
func dpapiProtect(plain []byte) ([]byte, error) {
	var out dataBlob
	r, _, err := procProtect.Call(
		uintptr(unsafe.Pointer(newBlob(plain))), 0,
		uintptr(unsafe.Pointer(newBlob(entropy))), 0, 0,
		cryptProtectUIForbidden, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, errors.New("CryptProtectData failed: " + err.Error())
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return out.bytes(), nil
}

// dpapiUnprotect reverses dpapiProtect.
func dpapiUnprotect(sealed []byte) ([]byte, error) {
	var out dataBlob
	r, _, err := procUnprotect.Call(
		uintptr(unsafe.Pointer(newBlob(sealed))), 0,
		uintptr(unsafe.Pointer(newBlob(entropy))), 0, 0,
		cryptProtectUIForbidden, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, errors.New("CryptUnprotectData failed: " + err.Error())
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return out.bytes(), nil
}
