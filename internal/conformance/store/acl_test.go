package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

type perm = ab2store.Permissions

var rwPerms = []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}

func aclOf(t *testing.T, s ab2store.Store, id int64) []ab2store.ACLEntry {
	t.Helper()
	var acl []ab2store.ACLEntry
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		acl, err = tx.ACL(ctx, id)
		return err
	})
	return acl
}

func setACL(s ab2store.Store, id int64, acl []ab2store.ACLEntry) error {
	return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error { return tx.SetACL(ctx, id, acl) })
}

func wantACL(t *testing.T, got, want []ab2store.ACLEntry) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ACL = %v, want %v", got, want)
	}
}

func wantPerms(t *testing.T, s ab2store.Store, id int64, who string, want perm) {
	t.Helper()
	var got perm
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		got, err = tx.EffectivePermissions(ctx, id, who)
		return err
	})
	if got != want {
		t.Errorf("EffectivePermissions(%s) = %+v, want %+v", who, got, want)
	}
}

func TestACLCreatorRecorded(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		ch := mustChannel(t, s, "c", ab2store.KindOrdinary, "pat", nil)
		if ch.Creator != "pat" {
			t.Errorf("Creator = %q, want pat", ch.Creator)
		}
	})
}

func TestACLDefaultRowsStoredWhenCreatedWithoutACL(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		want := []ab2store.ACLEntry{
			{Identity: "everyone", Permissions: rwPerms},
			{Identity: "pat", Permissions: []ab2store.Permission{"read", "write", "admin"}},
			{Identity: "admins", Permissions: []ab2store.Permission{"admin"}},
		}
		a := mustChannel(t, s, "a", ab2store.KindOrdinary, "pat", nil)
		b := mustChannel(t, s, "b", ab2store.KindOrdinary, "pat", ab2store.DefaultACL("pat"))
		wantACL(t, aclOf(t, s, a.ID), want)
		wantACL(t, aclOf(t, s, b.ID), want)
	})
}

func TestACLEmptyListStoresNoRows(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		mustIdentity(t, s, "kim")
		ch := mustChannel(t, s, "c", ab2store.KindOrdinary, "pat", []ab2store.ACLEntry{})
		wantACL(t, aclOf(t, s, ch.ID), nil)
		wantPerms(t, s, ch.ID, "kim", perm{})
		wantPerms(t, s, ch.ID, "pat", perm{Admin: true})
	})
}

func TestACLDefaultRowsAbsentWhenCreatedWithOwn(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		mustIdentity(t, s, "kim")
		own := []ab2store.ACLEntry{{Identity: "kim", Permissions: []ab2store.Permission{"read"}}}
		ch := mustChannel(t, s, "c", ab2store.KindOrdinary, "pat", own)
		wantACL(t, aclOf(t, s, ch.ID), own)
		wantPerms(t, s, ch.ID, "everyone", perm{})
	})
}

func TestACLImplicitEntriesGrantAdmin(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		mustIdentity(t, s, "kim")
		mustIdentity(t, s, "lee")
		mustAdmin(t, s, "root")
		ch := mustChannel(t, s, "c", ab2store.KindOrdinary, "pat", []ab2store.ACLEntry{{Identity: "kim", Permissions: []ab2store.Permission{"read"}}})
		wantPerms(t, s, ch.ID, "pat", perm{Admin: true})
		wantPerms(t, s, ch.ID, "root", perm{Admin: true})
		wantPerms(t, s, ch.ID, "kim", perm{Read: true})
		wantPerms(t, s, ch.ID, "lee", perm{})
		wantPerms(t, s, ch.ID, "ghost", perm{})
		err := s.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.EffectivePermissions(ctx, 9999, "pat")
			return err
		})
		if !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("unknown channel err = %v, want ErrNotFound", err)
		}
	})
}

func TestACLDuplicateEntriesAreAdditive(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "kim")
		acl := []ab2store.ACLEntry{
			{Identity: "kim", Permissions: []ab2store.Permission{"read"}},
			{Identity: "kim", Permissions: []ab2store.Permission{"write"}},
		}
		ch := mustChannel(t, s, "c", ab2store.KindOrdinary, "", acl)
		wantPerms(t, s, ch.ID, "kim", perm{Read: true, Write: true})
		wantACL(t, aclOf(t, s, ch.ID), acl)
	})
}

func TestACLRejectsUnknownPermissionAndEmptyList(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "kim")
		orig := []ab2store.ACLEntry{{Identity: "kim", Permissions: []ab2store.Permission{"read"}}}
		ch := mustChannel(t, s, "c", ab2store.KindOrdinary, "", orig)
		for name, acl := range map[string][]ab2store.ACLEntry{
			"unknown permission": {{Identity: "kim", Permissions: []ab2store.Permission{"owner"}}},
			"empty list":         {{Identity: "kim", Permissions: []ab2store.Permission{}}},
			"nil list":           {{Identity: "kim", Permissions: nil}},
			"valid then bad":     {{Identity: "everyone", Permissions: rwPerms}, {Identity: "kim", Permissions: nil}},
			"slash in identity":  {{Identity: "a/b", Permissions: []ab2store.Permission{"read"}}},
		} {
			if err := setACL(s, ch.ID, acl); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			}
			wantACL(t, aclOf(t, s, ch.ID), orig)
		}
		if err := setACL(s, 9999, orig); !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("unknown channel err = %v, want ErrNotFound", err)
		}
	})
}

func TestACLRejectsUnregisteredIdentity(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		ch := mustChannel(t, s, "c", ab2store.KindOrdinary, "", nil)
		bad := []ab2store.ACLEntry{{Identity: "ghost", Permissions: []ab2store.Permission{"read"}}}
		if err := setACL(s, ch.ID, bad); !errors.Is(err, ab2store.ErrInvalid) {
			t.Errorf("SetACL err = %v, want ErrInvalid", err)
		}
		wantACL(t, aclOf(t, s, ch.ID), ab2store.DefaultACL(""))
		// The same rejections inside one transaction that goes on to read: a
		// registry check that ran after the DELETE or between the inserts
		// would leave a changed ACL visible here, where a rollback cannot
		// hide it.
		for name, acl := range map[string][]ab2store.ACLEntry{
			"ghost alone":      bad,
			"valid then ghost": {{Identity: "everyone", Permissions: rwPerms}, bad[0]},
			"ghost then valid": {bad[0], {Identity: "everyone", Permissions: rwPerms}},
		} {
			err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				if err := tx.SetACL(ctx, ch.ID, acl); !errors.Is(err, ab2store.ErrInvalid) {
					t.Errorf("%s: SetACL err = %v, want ErrInvalid", name, err)
				}
				got, err := tx.ACL(ctx, ch.ID)
				if err != nil {
					return err
				}
				wantACL(t, got, ab2store.DefaultACL(""))
				return nil
			})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		special := []ab2store.ACLEntry{
			{Identity: "everyone", Permissions: []ab2store.Permission{"read"}},
			{Identity: "admins", Permissions: []ab2store.Permission{"admin"}},
		}
		if err := setACL(s, ch.ID, special); err != nil {
			t.Errorf("everyone/admins rejected: %v", err)
		}
		err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.CreateChannel(ctx, ab2store.ChannelCreate{Name: "d", Kind: ab2store.KindOrdinary, ACL: bad, CreatedAt: fixedNow})
			return err
		})
		if !errors.Is(err, ab2store.ErrInvalid) {
			t.Errorf("CreateChannel err = %v, want ErrInvalid", err)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.ChannelByName(ctx, "d"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("channel d exists: %v", err)
			}
			return nil
		})
	})
}

func TestACLRejectsNameWithSlash(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.CreateChannel(ctx, ab2store.ChannelCreate{Name: "a/b", Kind: ab2store.KindOrdinary, CreatedAt: fixedNow})
			return err
		})
		if !errors.Is(err, ab2store.ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})
}

func TestACLRejectsUnknownKind(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.CreateChannel(ctx, ab2store.ChannelCreate{Name: "x", Kind: "dm", CreatedAt: fixedNow})
			return err
		})
		if !errors.Is(err, ab2store.ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})
}

func TestACLGeneralHasDefaultAndNoCreator(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		ch := mustChannel(t, s, "general", ab2store.KindOrdinary, "", ab2store.DefaultACL(""))
		wantACL(t, aclOf(t, s, ch.ID), []ab2store.ACLEntry{
			{Identity: "everyone", Permissions: rwPerms},
			{Identity: "admins", Permissions: []ab2store.Permission{"admin"}},
		})
		if ch.Creator != "" {
			t.Errorf("Creator = %q, want empty", ch.Creator)
		}
		wantPerms(t, s, ch.ID, "pat", perm{Read: true, Write: true})
		// A creatorless channel stores creator '' (Decision 92): the empty
		// identity must not match it and gain admin.
		wantPerms(t, s, ch.ID, "", perm{Read: true, Write: true})
	})
}

func TestACLRemovingAdminsAndCreatorRowsKeepsImplicitAdmin(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		mustAdmin(t, s, "root")
		ch := mustChannel(t, s, "c", ab2store.KindOrdinary, "pat", nil)
		if err := setACL(s, ch.ID, []ab2store.ACLEntry{{Identity: "everyone", Permissions: rwPerms}}); err != nil {
			t.Fatal(err)
		}
		all := perm{Read: true, Write: true, Admin: true}
		wantPerms(t, s, ch.ID, "pat", all)
		wantPerms(t, s, ch.ID, "root", all)
		if err := setACL(s, ch.ID, []ab2store.ACLEntry{}); err != nil {
			t.Fatal(err)
		}
		wantACL(t, aclOf(t, s, ch.ID), nil)
		wantPerms(t, s, ch.ID, "pat", perm{Admin: true})
		wantPerms(t, s, ch.ID, "root", perm{Admin: true})
		if err := setACL(s, ch.ID, []ab2store.ACLEntry{{Identity: "pat", Permissions: []ab2store.Permission{"read"}}}); err != nil {
			t.Fatal(err)
		}
		wantPerms(t, s, ch.ID, "pat", perm{Read: true, Admin: true})
	})
}

func TestACLDeleteChannelRemovesEntries(t *testing.T) {
	TestChannelDeleteRemovesACL(t)
}

func TestACLReadableChannels(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "kim")
		mustAdmin(t, s, "root")
		read := []ab2store.Permission{"read"}
		c1 := mustChannel(t, s, "c1", ab2store.KindOrdinary, "", []ab2store.ACLEntry{{Identity: "kim", Permissions: read}})
		c2 := mustChannel(t, s, "c2", ab2store.KindOrdinary, "", []ab2store.ACLEntry{{Identity: "everyone", Permissions: read}})
		mustChannel(t, s, "c3", ab2store.KindOrdinary, "", []ab2store.ACLEntry{{Identity: "admins", Permissions: []ab2store.Permission{"admin"}}})
		// c4 stores a read row for admins: only an identity with is_admin
		// reads it (Decision 96), and EffectivePermissions must agree.
		c4 := mustChannel(t, s, "c4", ab2store.KindOrdinary, "", []ab2store.ACLEntry{{Identity: "admins", Permissions: read}})
		readable := func(who string) []int64 {
			var ids []int64
			mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
				ids, err = tx.ReadableChannels(ctx, who)
				return err
			})
			return ids
		}
		if got := readable("kim"); !reflect.DeepEqual(got, []int64{c1.ID, c2.ID}) {
			t.Errorf("kim = %v, want [%d %d]", got, c1.ID, c2.ID)
		}
		if got := readable("root"); !reflect.DeepEqual(got, []int64{c2.ID, c4.ID}) {
			t.Errorf("root = %v, want [%d %d]", got, c2.ID, c4.ID)
		}
		if got := readable("ghost"); !reflect.DeepEqual(got, []int64{c2.ID}) {
			t.Errorf("ghost = %v, want [%d]", got, c2.ID)
		}
		wantPerms(t, s, c4.ID, "root", perm{Read: true, Admin: true})
		wantPerms(t, s, c4.ID, "kim", perm{})
		wantPerms(t, s, c4.ID, "ghost", perm{})
	})
}
