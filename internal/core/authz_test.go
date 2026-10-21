package core

import (
	"context"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

func TestAuthorizeUsesImplicitEntries(t *testing.T) {
	c, _ := newTestCore(t, nil)
	mustIdentity(t, c, "pat", false)
	mustIdentity(t, c, "kim", false)
	// ACL names the creator's own row empty of permissions: only the
	// implicit creator-admin entry and the everyone read row apply.
	ch := mustChannel(t, c, store.ChannelCreate{
		Name: "work", Kind: store.KindOrdinary, Creator: "pat",
		ACL: []store.ACLEntry{{Identity: store.Everyone, Permissions: []store.Permission{store.PermRead}}},
	})
	check := func(identity string, p store.Permission) error {
		var err error
		mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
			err = c.Authorize(ctx, tx, identity, ch.ID, p)
			return nil
		})
		return err
	}
	if err := check("pat", store.PermAdmin); err != nil {
		t.Errorf("creator admin: %v", err)
	}
	if err := check("pat", store.PermWrite); bus.CodeOf(err) != bus.CodeForbidden {
		t.Errorf("creator write (implicit entry grants admin only) = %v, want forbidden", err)
	}
	if err := check("kim", store.PermRead); err != nil {
		t.Errorf("stranger read via everyone: %v", err)
	}
	err := check("kim", store.PermWrite)
	if bus.CodeOf(err) != bus.CodeForbidden {
		t.Fatalf("stranger write = %v, want forbidden", err)
	}
	if msg := err.(*bus.Error).Message; !strings.Contains(msg, "write") || !strings.Contains(msg, `"work"`) || !strings.Contains(msg, "kim") {
		t.Errorf("forbidden text %q does not name identity, permission and channel", msg)
	}
	if err.(*bus.Error).Retryable {
		t.Error("forbidden is retryable")
	}
}

func TestAuthorizeUnknownPermissionPanics(t *testing.T) {
	c, _ := newTestCore(t, nil)
	defer func() {
		if recover() == nil {
			t.Error("Authorize with an unknown permission did not panic")
		}
	}()
	_ = c.Authorize(context.Background(), nil, "pat", 1, store.Permission("owner"))
}

func TestRequireIdentityRejectsReservedAndUnknown(t *testing.T) {
	c, _ := newTestCore(t, nil)
	mustIdentity(t, c, "pat", false)
	for _, name := range []string{store.Everyone, store.Admins, "ghost", ""} {
		var err error
		mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
			_, err = c.RequireIdentity(ctx, tx, name)
			return nil
		})
		if bus.CodeOf(err) != bus.CodeNotRegistered {
			t.Errorf("RequireIdentity(%q) = %v, want not_registered", name, err)
		}
	}
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		id, err := c.RequireIdentity(ctx, tx, "pat")
		if err != nil || id.Name != "pat" {
			t.Errorf("RequireIdentity(pat) = %+v, %v", id, err)
		}
		return nil
	})
}

func TestRequireAdmin(t *testing.T) {
	c, _ := newTestCore(t, nil)
	mustIdentity(t, c, "pat", false)
	mustIdentity(t, c, "root", true)
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		if _, err := c.RequireAdmin(ctx, tx, "root"); err != nil {
			t.Errorf("root: %v", err)
		}
		_, err := c.RequireAdmin(ctx, tx, "pat")
		if bus.CodeOf(err) != bus.CodeForbidden || !strings.Contains(err.Error(), "administrator privilege required") {
			t.Errorf("pat = %v, want forbidden administrator privilege required", err)
		}
		if _, err := c.RequireAdmin(ctx, tx, "ghost"); bus.CodeOf(err) != bus.CodeNotRegistered {
			t.Errorf("ghost = %v, want not_registered", err)
		}
		return nil
	})
}

func TestChannelByNameAndReadable(t *testing.T) {
	c, _ := newTestCore(t, nil)
	mustIdentity(t, c, "pat", false)
	mustIdentity(t, c, "kim", false)
	open := mustChannel(t, c, store.ChannelCreate{Name: "open", Kind: store.KindOrdinary, Creator: "pat"})
	mustChannel(t, c, store.ChannelCreate{Name: "private", Kind: store.KindOrdinary, Creator: "pat",
		ACL: []store.ACLEntry{{Identity: "pat", Permissions: []store.Permission{store.PermRead}}}})
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		got, err := c.ChannelByName(ctx, tx, "open")
		if err != nil || got.ID != open.ID {
			t.Errorf("ChannelByName(open) = %+v, %v", got, err)
		}
		if _, err := c.ChannelByName(ctx, tx, "nope"); bus.CodeOf(err) != bus.CodeNotFound || !strings.Contains(err.Error(), `channel \"nope\" does not exist`) {
			t.Errorf("ChannelByName(nope) = %v", err)
		}
		ids, err := c.ReadableChannelIDs(ctx, tx, "kim")
		if err != nil || len(ids) != 1 || ids[0] != open.ID {
			t.Errorf("ReadableChannelIDs(kim) = %v, %v; want only the open channel", ids, err)
		}
		return nil
	})
}
