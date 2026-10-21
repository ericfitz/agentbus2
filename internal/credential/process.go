package credential

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// ProcessInput is the one JSON object written to a credential_process's stdin (Decision 56).
type ProcessInput struct {
	Version       int    `json:"version"`
	Identity      string `json:"identity"`
	TokenEndpoint string `json:"token_endpoint"`
	Audience      string `json:"audience"`
	JTI           string `json:"jti"`
}

// ProcessOutput is the one JSON object read from its stdout.
type ProcessOutput struct {
	Version         int    `json:"version"`
	PrivateKeyPEM   string `json:"private_key_pem,omitempty"`
	ClientAssertion string `json:"client_assertion,omitempty"`
	AccessToken     string `json:"access_token,omitempty"`
	ExpiresAt       string `json:"expires_at,omitempty"`
}

// ProcessTimeout bounds a credential_process run; processMaxOutput its stdout.
const (
	ProcessTimeout   = 10 * time.Second
	processMaxOutput = 64 * 1024
	processMaxStderr = 2048
)

// capWriter keeps at most max bytes and calls over once when more arrive.
type capWriter struct {
	buf  bytes.Buffer
	max  int
	over func()
	full bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); len(p) > room {
		w.buf.Write(p[:room])
		if !w.full {
			w.full = true
			w.over()
		}
	} else {
		w.buf.Write(p)
	}
	return len(p), nil
}

// RunProcess runs argv without a shell, writes in as JSON to its stdin and
// decodes its stdout strictly. Every error names argv[0]; none quotes stdout
// (it may hold a secret). Stderr is quoted, truncated.
func RunProcess(ctx context.Context, argv []string, in ProcessInput) (ProcessOutput, error) {
	if len(argv) == 0 || argv[0] == "" {
		return ProcessOutput{}, errors.New("credential_process: empty command")
	}
	name := argv[0]
	fail := func(format string, args ...any) (ProcessOutput, error) {
		return ProcessOutput{}, fmt.Errorf("credential_process %q: %s", name, fmt.Sprintf(format, args...))
	}
	in.Version = 1
	stdin, err := json.Marshal(in)
	if err != nil {
		return fail("encode input: %v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, ProcessTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, argv[1:]...) // #nosec G204 -- argv comes from the user's own config.json (Decision 33)
	cmd.Stdin = bytes.NewReader(stdin)
	out := &capWriter{max: processMaxOutput, over: cancel}
	errw := &capWriter{max: processMaxStderr, over: func() {}}
	cmd.Stdout, cmd.Stderr = out, errw
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	if out.full {
		return fail("output is larger than %d bytes", processMaxOutput)
	}
	if runErr != nil {
		if cerr := ctx.Err(); cerr != nil {
			return fail("%v", cerr)
		}
		if msg := strings.TrimSpace(errw.buf.String()); msg != "" {
			return fail("%v: stderr: %q", runErr, msg)
		}
		return fail("%v", runErr)
	}
	if out.buf.Len() == 0 {
		return fail("no output")
	}
	dec := json.NewDecoder(&out.buf)
	dec.DisallowUnknownFields()
	var o ProcessOutput
	if err := dec.Decode(&o); err != nil {
		return fail("output is not the expected JSON object: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fail("output has content after the JSON object")
	}
	if o.Version != 1 {
		return fail("unsupported output version %d (want 1)", o.Version)
	}
	n := 0
	for _, s := range []string{o.PrivateKeyPEM, o.ClientAssertion, o.AccessToken} {
		if s != "" {
			n++
		}
	}
	if n != 1 {
		return fail("credential_process must return exactly one of private_key_pem, client_assertion or access_token")
	}
	switch {
	case o.AccessToken != "":
		if o.ExpiresAt == "" {
			return fail("access_token needs expires_at")
		}
		if _, err := time.Parse(time.RFC3339, o.ExpiresAt); err != nil {
			return fail("expires_at is not RFC 3339: %v", err)
		}
	case o.ExpiresAt != "":
		return fail("expires_at is only valid with access_token")
	}
	return o, nil
}
