package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/store"
)

func TestValidateDefaultACL(t *testing.T) {
	if err := ValidateDefaultACL(shippedDefault()); err != nil {
		t.Errorf("shipped default rejected: %v", err)
	}
	if err := ValidateDefaultACL(nil); err != nil {
		t.Errorf("empty list rejected: %v", err)
	}
	cases := []struct {
		name    string
		entries []ACLEntry
		want    []string
	}{
		{"concrete name", []ACLEntry{{"ghost", []string{"read"}}}, []string{"default_acl", `"ghost"`}},
		{"slash", []ACLEntry{{"a/b", []string{"read"}}}, []string{"default_acl", `"a/b"`}},
		{"owner", []ACLEntry{{"owner", []string{"read"}}}, []string{"default_acl", `"owner"`}},
		{"duplicate permission", []ACLEntry{{"everyone", []string{"read", "read"}}}, []string{"default_acl", "entry 0", "duplicate"}},
		{"unknown permission", []ACLEntry{{"everyone", []string{"owner"}}}, []string{"default_acl", "entry 0", `"owner"`}},
		{"empty permissions", []ACLEntry{{"everyone", []string{"read"}}, {"admins", nil}}, []string{"default_acl", "entry 1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDefaultACL(tc.entries)
			if err == nil {
				t.Fatal("accepted")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

func TestDefaultACLFuncSubstitutes(t *testing.T) {
	f := DefaultACLFunc(shippedDefault())
	got := f("pat")
	want := []store.ACLEntry{
		{Identity: "everyone", Permissions: []store.Permission{"read", "write"}},
		{Identity: "pat", Permissions: []store.Permission{"read", "write", "admin"}},
		{Identity: "admins", Permissions: []store.Permission{"admin"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("f(pat) = %v, want %v", got, want)
	}
	if got := f(""); len(got) != 2 || got[0].Identity != "everyone" || got[1].Identity != "admins" {
		t.Errorf("f(\"\") = %v, want the creator entry dropped", got)
	}
	// The placeholder is not mutated across calls.
	if got := f("sam"); got[1].Identity != "sam" {
		t.Errorf("f(sam) = %v", got)
	}
}

func TestIsShippedDefault(t *testing.T) {
	if !IsShippedDefault(shippedDefault()) {
		t.Error("shipped literal not recognized")
	}
	s := shippedDefault()
	s[0], s[2] = s[2], s[0]
	if IsShippedDefault(s) {
		t.Error("reordered list recognized")
	}
	if IsShippedDefault(nil) {
		t.Error("empty list recognized")
	}
	s = shippedDefault()
	s[0].Permissions = []string{"write", "read"}
	if IsShippedDefault(s) {
		t.Error("permission order ignored")
	}
}
