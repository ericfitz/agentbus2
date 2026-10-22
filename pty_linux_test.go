package main

import (
	"fmt"

	"golang.org/x/sys/unix"
)

const ioctlGetTermios = unix.TCGETS

// ptsName unlocks the slave of the master fd and returns its path.
func ptsName(fd int) (string, error) {
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		return "", fmt.Errorf("TIOCSPTLCK: %w", err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		return "", fmt.Errorf("TIOCGPTN: %w", err)
	}
	return fmt.Sprintf("/dev/pts/%d", n), nil
}
