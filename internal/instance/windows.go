//go:build windows

package instance

import (
	"os"
	"syscall"
	"unsafe"
)

var lockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func acquire(f *os.File) (bool, error) {
	var overlapped syscall.Overlapped
	ok, _, err := lockFileEx.Call(f.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if ok != 0 {
		return true, nil
	}
	if err == syscall.Errno(33) {
		return false, nil
	}
	return false, err
}
