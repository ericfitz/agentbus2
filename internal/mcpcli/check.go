package mcpcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/client"
	"github.com/ericfitz/agentmesh/internal/credential"
	"github.com/ericfitz/agentmesh/internal/fileacl"
	"github.com/ericfitz/agentmesh/internal/mcpserver"
	"github.com/ericfitz/agentmesh/internal/profile"
)

// checkTimeout bounds each network step of `check`.
const checkTimeout = 15 * time.Second

// target is the server a command works on, found before any network or key
// access (open note: mcp-only-client 8). The code is -1 to proceed, else the
// exit code, with the message already on stderr.
func target(cmdName string, o client.Options, stderr io.Writer) (server string, code int) {
	exit := func(err error) (string, int) {
		_, _ = fmt.Fprintf(stderr, "%s %s: %v\n", Binary, cmdName, err)
		return "", 2
	}
	sel, err := client.SelectProfile(o)
	if err != nil {
		return exit(err)
	}
	want := strings.TrimSuffix(o.Server, "/")
	switch {
	case sel.Defined && sel.Profile.Mode != profile.ModeServer:
		return exit(client.NoLocalModeError(sel.Name))
	case sel.Defined && want != "" && want != sel.Profile.Server:
		return exit(fmt.Errorf("profile %q uses server %s; -server %s disagrees", sel.Name, sel.Profile.Server, o.Server))
	case sel.Defined:
		return sel.Profile.Server, -1
	case want != "":
		return want, -1
	case sel.Name == "default":
		return exit(client.NoLocalModeError(sel.Name))
	}
	return exit(fmt.Errorf("profile %q is not defined in %s: pass -server URL, or run agentmesh-mcponly register -server URL -identity NAME", sel.Name, sel.Config.Path))
}

// checkReport prints the steps of `check`.
type checkReport struct {
	out  io.Writer
	errw io.Writer
}

func (c *checkReport) ok(step, format string, a ...any) {
	_, _ = fmt.Fprintf(c.out, "ok   %s: %s\n", step, fmt.Sprintf(format, a...))
}

// fail prints the FAIL line and the stderr summary and returns exit code 1.
func (c *checkReport) fail(step, detail, fix string) int {
	_, _ = fmt.Fprintf(c.out, "FAIL %s: %s; fix: %s\n", step, oneLine(detail), fix)
	_, _ = fmt.Fprintf(c.errw, "%s: check: %s failed\n", Binary, step)
	return 1
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// codeDetail is "<bus code>: <message>" for a bus error, else the text.
func codeDetail(err error) string {
	var be *bus.Error
	if errors.As(err, &be) {
		return fmt.Sprintf("%s: %s", be.Code, be.Message)
	}
	return err.Error()
}

// busFix is the fix for a failed bus call after the credential loaded.
func busFix(err error, server, identity string) string {
	switch bus.CodeOf(err) {
	case bus.CodeUnauthenticated:
		return fmt.Sprintf("%s register -server %s -identity %s (if %s already holds another key, an administrator runs agentmesh server admin reset-credential %s first)", Binary, server, identity, identity, identity)
	case bus.CodeUnavailable:
		return "retry later; the server is busy or its store is down"
	}
	return Binary + " help troubleshoot"
}

// resolveFix is the fix for a resolution error of the identity step, by cause;
// an unrecognized error points at the troubleshooting topic.
func resolveFix(err error) string {
	if errors.Is(err, client.ErrCredentialProcessConflict) {
		return "drop -credential, or remove credential_process from the profile in config.json"
	}
	return Binary + " help troubleshoot"
}

// ownerOnlyFix is the command that makes a key file owner-only.
func ownerOnlyFix(path string) string {
	if runtime.GOOS == "windows" {
		return fileacl.WindowsFix(path)
	}
	return "chmod 600 " + path
}

// runCheck is `agentmesh-mcponly check [-server URL] [-identity I]
// [-credential FILE] [-profile P]`: reach, health, identity, credential,
// token and access, one line each, stopping at the first FAIL (exit 1). It
// creates no session and changes no configuration; the token step always
// exchanges (never uses a cached bearer) and caches the new bearer in the data
// directory, as serve does. A second or overlapping run does the same. Without an identity it stops after health with
// the register command to run next (exit 0).
func runCheck(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("check", stderr)
	o := client.Options{Environ: environ}
	commonFlags(fs, &o)
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "%s check: unexpected argument %q\n", Binary, fs.Arg(0))
		return 2
	}
	applyEnv(&o, environ)
	server, code := target("check", o, stderr)
	if code != -1 {
		return code
	}
	if o.Server == "" {
		o.Server = server
	}
	rep := &checkReport{out: stdout, errw: stderr}
	hc := &http.Client{Timeout: checkTimeout}
	o.HTTP = hc

	info, err := client.DiscoverServer(ctx, hc, server)
	if err != nil {
		return rep.fail("reach", err.Error(), fmt.Sprintf("check the URL and that the server is running, then %s check -server %s", Binary, server))
	}
	rep.ok("reach", "%s (server %s, protocol %s)", server, info.ServerVersion, info.ProtocolVersion)

	h, err := client.ProbeHealth(ctx, hc, server)
	if err != nil {
		return rep.fail("health", codeDetail(err), "ask the server's operator: its store does not answer")
	}
	rep.ok("health", "%s (%s tier)", h.Status, h.Tier)

	r, err := client.Resolve(o)
	if err != nil {
		if errors.Is(err, profile.ErrIdentityRequired) {
			_, _ = fmt.Fprintf(stdout, "next: %s register -server %s -identity NAME\n", Binary, server)
			return 0
		}
		return rep.fail("identity", err.Error(), resolveFix(err))
	}
	identity := r.Identity
	// Decision 16: serve exits 1 for the OS login name, so check says so first.
	if err := mcpserver.IdentityGuard(o, osUserName(), Binary+" serve"); err != nil {
		return rep.fail("identity", err.Error(), fmt.Sprintf("register another name, not the OS login name, with %s register -server %s -identity NAME, and use that name in the harness's MCP config", Binary, server))
	}
	rep.ok("identity", "%s (%s)", identity, r.IdentitySource)

	registerFix := fmt.Sprintf("%s register -server %s -identity %s", Binary, server, identity)
	switch {
	case o.Credential != "":
		if _, _, err := credential.LoadFile(o.Credential); err != nil {
			return rep.fail("credential", err.Error(), credentialFix(err, o.Credential, registerFix))
		}
		rep.ok("credential", "%s", o.Credential)
	case len(r.Profile.CredentialProcess) > 0:
		rep.ok("credential", "credential_process %s", r.Profile.CredentialProcess[0])
	case !r.Credentials.Exists(identity):
		return rep.fail("credential", fmt.Sprintf("no credential for %q in profile %q", identity, r.Profile.Name), registerFix)
	default:
		path := r.Credentials.Path(identity)
		if _, _, err := r.Credentials.Load(identity); err != nil {
			return rep.fail("credential", err.Error(), credentialFix(err, path, registerFix))
		}
		rep.ok("credential", "%s", path)
	}

	conn, err := client.Open(ctx, o)
	if err != nil {
		return rep.fail("token", err.Error(), Binary+" help troubleshoot")
	}
	defer func() { _ = conn.Close() }()
	if conn.Source == nil {
		return rep.fail("token", "the connection has no token source", Binary+" help troubleshoot")
	}
	if _, err := conn.Source.Refresh(ctx); err != nil {
		return rep.fail("token", codeDetail(err), busFix(err, server, identity))
	}
	rep.ok("token", "exchanged a bearer for %s", identity)

	ah, err := conn.Bus.Health(ctx)
	if err != nil {
		return rep.fail("access", codeDetail(err), busFix(err, server, identity))
	}
	if ah.Tier != bus.TierIdentity && ah.Tier != bus.TierAdmin {
		return rep.fail("access", fmt.Sprintf("the server answered at the %s tier, not the identity tier", ah.Tier), Binary+" help troubleshoot")
	}
	rep.ok("access", "health at the %s tier", ah.Tier)
	return 0
}

// credentialFix is the fix for a credential file that did not load.
func credentialFix(err error, path, registerFix string) string {
	if errors.Is(err, credential.ErrNotOwnerOnly) {
		return ownerOnlyFix(path)
	}
	return registerFix
}
