package oidcclient

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// OpenBrowser opens url in the system browser without a shell: open (darwin),
// rundll32 url.dll,FileProtocolHandler (windows) or xdg-open (others). It
// refuses anything but an http or https URL, so a hand-built Config cannot turn
// it into a file or program launcher.
func OpenBrowser(url string) error {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return errors.New("oidcclient: refusing to open a non-http URL")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap the child
	return nil
}
