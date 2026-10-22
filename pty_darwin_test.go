package main

import (
	"bytes"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

const ioctlGetTermios = unix.TIOCGETA

// ptsName grants and unlocks the slave of the master fd and returns its path.
// ptsname(3) needs cgo (the build is CGO_ENABLED=0) and x/sys exports no
// pointer ioctl for TIOCPTYGNAME, so that one is a raw syscall.
func ptsName(fd int) (string, error) {
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		return "", fmt.Errorf("TIOCPTYGRANT: %w", err)
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		return "", fmt.Errorf("TIOCPTYUNLK: %w", err)
	}
	buf := make([]byte, 128) // the size TIOCPTYGNAME is defined with
	ptr := uintptr(unsafe.Pointer(&buf[0]))
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCPTYGNAME, ptr); errno != 0 { //nolint:staticcheck // see above
		return "", fmt.Errorf("TIOCPTYGNAME: %w", errno)
	}
	name, _, _ := bytes.Cut(buf, []byte{0})
	return string(name), nil
}
