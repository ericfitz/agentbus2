package store

import (
	"errors"
	"reflect"
	"testing"
)

func TestValidateEntry(t *testing.T) {
	ok := []ACLEntry{
		{Everyone, []Permission{PermRead}},
		{Admins, []Permission{PermAdmin}},
		{"pat", []Permission{PermRead, PermWrite, PermAdmin}},
	}
	for _, e := range ok {
		if err := ValidateEntry(e); err != nil {
			t.Errorf("ValidateEntry(%v) = %v, want nil", e, err)
		}
	}
	bad := map[string]ACLEntry{
		"nil permissions":     {"pat", nil},
		"empty permissions":   {"pat", []Permission{}},
		"unknown permission":  {"pat", []Permission{"owner"}},
		"wrong case":          {"pat", []Permission{"Read"}},
		"unknown among valid": {"pat", []Permission{PermRead, "owner"}},
		"slash identity":      {"a/b", []Permission{PermRead}},
		"empty identity":      {"", []Permission{PermRead}},
		"non-UTF-8 identity":  {"p\xff", []Permission{PermRead}},
	}
	for name, e := range bad {
		t.Run(name, func(t *testing.T) {
			err := ValidateEntry(e)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateEntry(%v) = %v, want ErrInvalid", e, err)
			}
		})
	}
}

func TestDefaultACL(t *testing.T) {
	got := DefaultACL("pat")
	want := []ACLEntry{
		{Everyone, []Permission{PermRead, PermWrite}},
		{"pat", []Permission{PermRead, PermWrite, PermAdmin}},
		{Admins, []Permission{PermAdmin}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultACL(pat) = %v, want %v", got, want)
	}
	got = DefaultACL("")
	want = []ACLEntry{want[0], want[2]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultACL(\"\") = %v, want %v", got, want)
	}
}

func TestPermissionsUnion(t *testing.T) {
	r := Permissions{Read: true}
	w := Permissions{Write: true}
	if got := r.Union(w); got != (Permissions{Read: true, Write: true}) {
		t.Errorf("Union = %v", got)
	}
	if got := (Permissions{}).Union(Permissions{}); got != (Permissions{}) {
		t.Errorf("zero Union = %v", got)
	}
	if got := r.Union(Permissions{Admin: true}); got != (Permissions{Read: true, Admin: true}) {
		t.Errorf("Union = %v", got)
	}
}

func TestValidPermission(t *testing.T) {
	for _, p := range []Permission{PermRead, PermWrite, PermAdmin} {
		if !ValidPermission(p) {
			t.Errorf("ValidPermission(%q) = false", p)
		}
	}
	for _, p := range []Permission{"", "owner", "Read"} {
		if ValidPermission(p) {
			t.Errorf("ValidPermission(%q) = true", p)
		}
	}
}
