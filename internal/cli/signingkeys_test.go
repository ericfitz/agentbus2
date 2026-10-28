package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
)

// fakeKeysBus answers the signing key operations from canned results and
// records the rotate input; the rest of the Bus is the opened one.
type fakeKeysBus struct {
	bus.Bus
	keys []bus.SigningKey
	res  bus.SigningKeyRotation
	err  error
	got  bus.RotateSigningKeyInput
}

func (f *fakeKeysBus) ListSigningKeys(context.Context) ([]bus.SigningKey, error) {
	return f.keys, f.err
}

func (f *fakeKeysBus) RotateSigningKey(_ context.Context, in bus.RotateSigningKeyInput) (bus.SigningKeyRotation, error) {
	f.got = in
	return f.res, f.err
}

func useFakeKeys(t *testing.T) *fakeKeysBus {
	t.Helper()
	fake := &fakeKeysBus{}
	testConn = func(c *client.Conn) { fake.Bus = c.Bus; c.Bus = fake }
	t.Cleanup(func() { testConn = nil })
	return fake
}

func TestSigningKeysList(t *testing.T) {
	e := adminEnv(t)
	fake := useFakeKeys(t)
	run := runner(e, RunSigningKeys)
	fake.keys = []bus.SigningKey{
		{KID: "K1", State: "active", Provider: "store", Algorithm: "EdDSA", CreatedAt: 5},
		{KID: "K0", State: "retiring", Provider: "awskms", ProviderRef: "awskms:///alias/x", Algorithm: "ES256", CreatedAt: 1, RetiredAt: 5, RemoveAfter: 99},
	}
	code, out, er := run("list")
	if code != 0 {
		t.Fatalf("list: %d %s", code, er)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 2 {
		t.Fatalf("out %q: %v", out, err)
	}
	want0 := map[string]any{"kid": "K1", "state": "active", "provider": "store", "provider_ref": "", "algorithm": "EdDSA", "created_at": float64(5), "retired_at": float64(0), "remove_after": float64(0)}
	w0, _ := json.Marshal(want0)
	g0, _ := json.Marshal(got[0])
	if string(w0) != string(g0) {
		t.Fatalf("key 0 = %s\nwant %s", g0, w0)
	}
	if got[1]["provider_ref"] != "awskms:///alias/x" || got[1]["remove_after"] != float64(99) {
		t.Fatalf("key 1 = %v", got[1])
	}
	if strings.Contains(strings.ToLower(out), "private") {
		t.Fatalf("out names a private key: %s", out)
	}

	// An empty set prints [], never null.
	fake.keys = nil
	if code, out, _ = run("list"); code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty list: %d %q", code, out)
	}
}

func TestSigningKeysRotate(t *testing.T) {
	e := adminEnv(t)
	fake := useFakeKeys(t)
	run := runner(e, RunSigningKeys)
	fake.res = bus.SigningKeyRotation{
		Active:   bus.SigningKey{KID: "K2", State: "active", Provider: "store", Algorithm: "EdDSA", CreatedAt: 9},
		Previous: bus.SigningKey{KID: "K1", State: "retiring", Provider: "store", Algorithm: "EdDSA", CreatedAt: 5, RetiredAt: 9, RemoveAfter: 99},
	}
	code, out, er := run("rotate")
	if code != 0 {
		t.Fatalf("rotate: %d %s", code, er)
	}
	if fake.got.RevokePrevious || fake.got.IdempotencyKey != "" {
		t.Fatalf("input = %+v", fake.got)
	}
	var got struct {
		Active   map[string]any `json:"active"`
		Previous map[string]any `json:"previous"`
		Revoked  bool           `json:"revoked"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Active["kid"] != "K2" || got.Previous["kid"] != "K1" || got.Revoked {
		t.Fatalf("out %q: %v", out, err)
	}

	fake.res.Revoked = true
	code, out, er = run("rotate", "-revoke-previous", "-key", "K")
	if code != 0 || !fake.got.RevokePrevious || fake.got.IdempotencyKey != "K" || !strings.Contains(out, `"revoked": true`) {
		t.Fatalf("revoking rotate: %d %q %s; input %+v", code, out, er, fake.got)
	}

	// No previous key: the member is absent.
	fake.res = bus.SigningKeyRotation{Active: fake.res.Active}
	code, out, _ = run("rotate")
	if code != 0 || strings.Contains(out, `"previous"`) {
		t.Fatalf("first rotate: %d %s", code, out)
	}
}

func TestSigningKeysBusError(t *testing.T) {
	e := adminEnv(t)
	fake := useFakeKeys(t)
	run := runner(e, RunSigningKeys)
	fake.err = bus.Errorf(bus.CodeConflict, false, "inside the cooldown")
	for _, args := range [][]string{{"list"}, {"rotate"}} {
		code, out, er := run(args...)
		if code != 1 || out != "" || !strings.Contains(er, "agentbus2: conflict: inside the cooldown") {
			t.Fatalf("%v: %d %q %q", args, code, out, er)
		}
	}
}

// On a local profile there are no signing keys; the conformance suite and the
// server answer the same way.
func TestSigningKeysCommandsUnavailableLocally(t *testing.T) {
	e := adminEnv(t)
	run := runner(e, RunSigningKeys)
	for _, args := range [][]string{{"list"}, {"rotate"}, {"rotate", "-revoke-previous"}} {
		code, _, er := run(args...)
		if code != 1 || !strings.Contains(er, "agentbus2: unavailable: "+bus.SigningKeysNotConfigured) {
			t.Fatalf("%v: %d %q", args, code, er)
		}
	}
}

func TestSigningKeysUsage(t *testing.T) {
	e := adminEnv(t)
	run := runner(e, RunSigningKeys)
	for _, args := range [][]string{{}, {"frob"}, {"list", "extra"}, {"list", "-revoke-previous"}, {"rotate", "extra"}, {"rotate", "-from", "x"}} {
		if code, _, _ := run(args...); code != 2 {
			t.Fatalf("%v: exit %d, want 2", args, code)
		}
	}
}
