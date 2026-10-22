package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"

	"github.com/ericfitz/agentbus2/internal/fileacl"
	"github.com/ericfitz/agentbus2/internal/filelock"
)

// lookupIPAddr resolves a listen hostname; tests replace it.
var lookupIPAddr = net.DefaultResolver.LookupIPAddr

// ClassifyListen reports whether a listen address is local (Milestone 5
// choice 10, Decisions 20 and 58). "unix:<path>" is local; host:port is split
// and an empty, 0.0.0.0 or :: host is not local; an IP is local when it is a
// loopback address (::ffff:127.0.0.1 included); a hostname is local when it
// resolves and every address is loopback. A hostname that does not resolve,
// and an address that does not parse, are errors.
func ClassifyListen(listen string) (bool, error) {
	if path, ok := strings.CutPrefix(listen, "unix:"); ok {
		if path == "" {
			return false, errors.New("listen address \"unix:\" has no socket path")
		}
		return true, nil
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false, fmt.Errorf("listen address %q: %w", listen, err)
	}
	if host == "" {
		return false, nil
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		return !addr.IsUnspecified() && addr.IsLoopback(), nil
	}
	ips, err := lookupIPAddr(context.Background(), host)
	if err != nil {
		return false, fmt.Errorf("listen host %q does not resolve: %w", host, err)
	}
	if len(ips) == 0 {
		return false, fmt.Errorf("listen host %q resolved to no address", host)
	}
	for _, ip := range ips {
		if !ip.IP.IsLoopback() {
			return false, nil
		}
	}
	return true, nil
}

// Listen opens the listener. A "unix:<path>" address first removes a stale
// socket file nothing answers on, then listens and sets the file's mode to
// 0600, and on Windows its DACL to the current user alone (Decision 58: the
// socket file, not the umask, decides who may self-register on a multi-user
// host; the window between net.Listen and the Chmod and ACL calls is bounded
// by the umask (on Windows, the directory's inherited ACL) and accepted; a
// lock file at path+".lock" serializes concurrent starts).
func Listen(listen string) (net.Listener, error) {
	path, ok := strings.CutPrefix(listen, "unix:")
	if !ok {
		return net.Listen("tcp", listen)
	}
	if path == "" {
		return nil, errors.New("listen address \"unix:\" has no socket path")
	}
	// Serialize probe, remove, listen and chmod across processes, so two
	// servers starting on one stale path cannot both take it over (the
	// second would unlink the first's live socket). The lock file is left in
	// place: removing it would reopen the race. Once a listener is live, a
	// later probe connects and is refused as in use.
	lf, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening the socket lock: %w", err)
	}
	defer func() { _ = lf.Close() }()
	if err := filelock.Lock(lf); err != nil {
		return nil, fmt.Errorf("locking %s: %w", lf.Name(), err)
	}
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("setting the mode of socket %s: %w", path, err)
	}
	// On Windows the mode above does not restrict access (os.Chmod only
	// toggles the read-only attribute): the owner-only DACL does.
	if err := fileacl.OwnerOnly(path); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("setting the ACL of socket %s: %w", path, err)
	}
	return l, nil
}

// beforeStaleRemove is a test hook run after a socket probes as stale and
// before it is removed.
var beforeStaleRemove = func() {}

// removeStaleSocket deletes path when it is a socket file that refuses
// connections. A live socket and a file of any other kind are left alone
// and reported as errors.
func removeStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s exists and is not a socket", path)
	}
	c, err := net.Dial("unix", path)
	if err == nil {
		_ = c.Close()
		return fmt.Errorf("%s is in use by a running listener", path)
	}
	if !isStaleDialError(err) {
		return fmt.Errorf("probing socket %s: %w", path, err)
	}
	beforeStaleRemove()
	if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return rerr
	}
	return nil
}

// isStaleDialError reports whether a dial error says nothing listens on the
// socket file (staleDialErrors, per platform). Any other error, such as a
// permission error, leaves the file alone.
func isStaleDialError(err error) bool {
	for _, target := range staleDialErrors {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
