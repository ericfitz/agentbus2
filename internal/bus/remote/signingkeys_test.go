package remote

import (
	"context"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

const (
	activeKeyJSON   = `{"kid":"K1","state":"active","provider":"store","provider_ref":"","algorithm":"EdDSA","created_at":5,"retired_at":0,"remove_after":0}`
	retiringKeyJSON = `{"kid":"K0","state":"retiring","provider":"awskms","provider_ref":"awskms:///alias/x","algorithm":"ES256","created_at":1,"retired_at":5,"remove_after":99}`
)

func TestRemoteListSigningKeys(t *testing.T) {
	rec := &recorder{status: 200, ctype: "application/json", body: `{"keys":[` + activeKeyJSON + `,` + retiringKeyJSON + `]}`}
	ks, err := New(newRecClient(t, rec)).ListSigningKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != "GET" || rec.path != "/v1/admin/signing-keys" {
		t.Fatalf("request %s %s", rec.method, rec.path)
	}
	want := []bus.SigningKey{
		{KID: "K1", State: "active", Provider: "store", Algorithm: "EdDSA", CreatedAt: 5},
		{KID: "K0", State: "retiring", Provider: "awskms", ProviderRef: "awskms:///alias/x", Algorithm: "ES256", CreatedAt: 1, RetiredAt: 5, RemoveAfter: 99},
	}
	if len(ks) != 2 || ks[0] != want[0] || ks[1] != want[1] {
		t.Fatalf("keys = %+v\nwant %+v", ks, want)
	}
	// An empty set is an empty slice, not an error.
	rec = &recorder{status: 200, ctype: "application/json", body: `{"keys":[]}`}
	if ks, err = New(newRecClient(t, rec)).ListSigningKeys(context.Background()); err != nil || len(ks) != 0 {
		t.Fatalf("empty = %+v, %v", ks, err)
	}
}

func TestRemoteRotateSigningKey(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{status: 200, ctype: "application/json", body: `{"active":` + activeKeyJSON + `,"previous":` + retiringKeyJSON + `,"revoked":false}`}
	b := New(newRecClient(t, rec))
	r, err := b.RotateSigningKey(ctx, bus.RotateSigningKeyInput{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != "POST" || rec.path != "/v1/admin/signing-keys/rotate" || strings.Contains(rec.query, "revoke_previous=true") || rec.header.Get("Idempotency-Key") != "" {
		t.Fatalf("request %s %s?%s %v", rec.method, rec.path, rec.query, rec.header)
	}
	if r.Active.KID != "K1" || r.Previous.KID != "K0" || r.Previous.State != "retiring" || r.Previous.RemoveAfter != 99 || r.Revoked {
		t.Fatalf("rotation = %+v", r)
	}

	rec.body = `{"active":` + activeKeyJSON + `,"previous":` + retiringKeyJSON + `,"revoked":true}`
	r, err = b.RotateSigningKey(ctx, bus.RotateSigningKeyInput{RevokePrevious: true, IdempotencyKey: "kk"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.query, "revoke_previous=true") || rec.header.Get("Idempotency-Key") != "kk" || !r.Revoked {
		t.Fatalf("revoking request: %s %v; rotation %+v", rec.query, rec.header, r)
	}

	// The first key has no previous member: the zero SigningKey.
	rec.body = `{"active":` + activeKeyJSON + `,"revoked":false}`
	r, err = b.RotateSigningKey(ctx, bus.RotateSigningKeyInput{})
	if err != nil || r.Previous != (bus.SigningKey{}) || r.Active.KID != "K1" {
		t.Fatalf("first rotation = %+v, %v", r, err)
	}
}

func TestRemoteSigningKeysProblem(t *testing.T) {
	rec := &recorder{status: 409, ctype: "application/problem+json",
		body: `{"type":"about:blank","title":"Conflict","status":409,"code":"conflict","detail":"cooldown","retryable":false}`}
	b := New(newRecClient(t, rec))
	_, err := b.RotateSigningKey(context.Background(), bus.RotateSigningKeyInput{})
	if bus.CodeOf(err) != bus.CodeConflict || !strings.Contains(err.Error(), "cooldown") {
		t.Fatalf("rotate err = %v", err)
	}
	if _, err = b.ListSigningKeys(context.Background()); bus.CodeOf(err) != bus.CodeConflict {
		t.Fatalf("list err = %v", err)
	}
}

// A closed-enum member outside its set, as a server speaking another protocol
// would send it, is an internal error naming the member, on both operations.
func TestRemoteSigningKeysRefuseUnknownEnum(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct{ member, bad string }{
		{"state", strings.Replace(activeKeyJSON, `"state":"active"`, `"state":"paused"`, 1)},
		{"provider", strings.Replace(activeKeyJSON, `"provider":"store"`, `"provider":"vault"`, 1)},
		{"algorithm", strings.Replace(activeKeyJSON, `"algorithm":"EdDSA"`, `"algorithm":"RS256"`, 1)},
	} {
		t.Run(tc.member, func(t *testing.T) {
			rec := &recorder{status: 200, ctype: "application/json", body: `{"keys":[` + tc.bad + `]}`}
			_, err := New(newRecClient(t, rec)).ListSigningKeys(ctx)
			wantInternal(t, err, tc.member)
			rec = &recorder{status: 200, ctype: "application/json", body: `{"active":` + tc.bad + `,"revoked":false}`}
			_, err = New(newRecClient(t, rec)).RotateSigningKey(ctx, bus.RotateSigningKeyInput{})
			wantInternal(t, err, tc.member)
			rec = &recorder{status: 200, ctype: "application/json", body: `{"active":` + activeKeyJSON + `,"previous":` + tc.bad + `,"revoked":false}`}
			_, err = New(newRecClient(t, rec)).RotateSigningKey(ctx, bus.RotateSigningKeyInput{})
			wantInternal(t, err, tc.member)
		})
	}
}
