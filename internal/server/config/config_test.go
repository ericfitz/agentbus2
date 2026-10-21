package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
)

func valid() Config {
	c := Default()
	c.SQLite = "/tmp/x.db"
	return c
}

func goodIssuer() Issuer {
	return Issuer{
		Issuer: "https://idp.example", JWKSURI: "https://idp.example/jwks", Audience: "agentbus",
		Algorithms: []string{"ES256"}, Subjects: map[string]string{"sub1": "pat"},
	}
}

func TestDefaultMatchesCore(t *testing.T) {
	got, err := Default().Core()
	if err != nil {
		t.Fatal(err)
	}
	want := core.Defaults()
	got.DefaultACL, want.DefaultACL = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Default().Core() = %+v, want %+v", got, want)
	}
	d := Default()
	if d.TokenLifetimeSeconds != 3600 || d.JWKSRefreshSeconds != 300 || d.ChannelCreation != "open" || d.LogLevel != "info" {
		t.Errorf("unexpected defaults: %+v", d)
	}
	if d.Listen != "" || d.Registration != "" || d.SQLite != "" || d.Postgres != "" || d.Issuer != "" {
		t.Errorf("Listen, Registration, SQLite, Postgres and Issuer must default empty: %+v", d)
	}
	if err := valid().Validate(); err != nil {
		t.Errorf("valid default with sqlite rejected: %v", err)
	}
}

func TestSettingsListsEveryJSONName(t *testing.T) {
	var want []string
	typ := reflect.TypeFor[Config]()
	for i := range typ.NumField() {
		want = append(want, strings.Split(typ.Field(i).Tag.Get("json"), ",")[0])
	}
	if got := Settings(); !slices.Equal(got, want) {
		t.Errorf("Settings() = %v, want %v", got, want)
	}
	for _, n := range []string{"empty_channel_reap_hours", "default_acl", "issuers"} {
		if !slices.Contains(Settings(), n) {
			t.Errorf("Settings() lacks %s", n)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"both stores", func(c *Config) { c.Postgres = "postgres://x" }, "exactly one of sqlite or postgres"},
		{"no store", func(c *Config) { c.SQLite = "" }, "exactly one of sqlite or postgres"},
		{"registration", func(c *Config) { c.Registration = "tofu" }, "registration"},
		{"issuer path", func(c *Config) { c.Issuer = "http://x/base" }, "issuer"},
		{"issuer slash", func(c *Config) { c.Issuer = "http://x/" }, "issuer"},
		{"issuer query", func(c *Config) { c.Issuer = "http://x?a=b" }, "issuer"},
		{"issuer scheme", func(c *Config) { c.Issuer = "ftp://x" }, "issuer"},
		{"token lifetime", func(c *Config) { c.TokenLifetimeSeconds = 0 }, "token_lifetime_seconds"},
		{"jwks refresh", func(c *Config) { c.JWKSRefreshSeconds = 5 }, "jwks_refresh_seconds"},
		{"no audience", func(c *Config) { i := goodIssuer(); i.Audience = ""; c.Issuers = []Issuer{i} }, "audience"},
		{"bad algorithm", func(c *Config) { i := goodIssuer(); i.Algorithms = []string{"HS256"}; c.Issuers = []Issuer{i} }, "algorithm"},
		{"subject everyone", func(c *Config) {
			i := goodIssuer()
			i.Subjects = map[string]string{"s": "everyone"}
			c.Issuers = []Issuer{i}
		}, "subjects"},
		{"subject slash", func(c *Config) {
			i := goodIssuer()
			i.Subjects = map[string]string{"s": "a/b"}
			c.Issuers = []Issuer{i}
		}, "subjects"},
		{"no subjects", func(c *Config) { i := goodIssuer(); i.Subjects = nil; c.Issuers = []Issuer{i} }, "subjects"},
		{"jwks uri", func(c *Config) { i := goodIssuer(); i.JWKSURI = "jwks"; c.Issuers = []Issuer{i} }, "jwks_uri"},
		{"channel creation", func(c *Config) { c.ChannelCreation = "closed" }, "channel_creation"},
		{"log level", func(c *Config) { c.LogLevel = "trace" }, "log_level"},
		{"embedding no model", func(c *Config) { c.EmbeddingEndpoint = "http://e" }, "embedding_model"},
		{"embedding relative", func(c *Config) { c.EmbeddingEndpoint = "e"; c.EmbeddingModel = "m" }, "embedding_endpoint"},
		{"both keys", func(c *Config) { c.EmbeddingAPIKeyFile = "f"; c.EmbeddingAPIKeyEnv = "E" }, "embedding_api_key"},
		{"receive counts", func(c *Config) { c.ReceiveDefaultCount = 20; c.ReceiveMaxCount = 10 }, "receive_default_count"},
		{"core bound", func(c *Config) { c.MaxMessageKiB = 0 }, "max_message_kib"},
		{"reap bound", func(c *Config) { c.EmptyChannelReapHours = 0 }, "empty_channel_reap_hours"},
		{"default acl", func(c *Config) { c.DefaultACL = []ACLEntry{{"ghost", []string{"read"}}} }, "default_acl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mut(&c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}
	// Listen is never required, and a good issuer passes.
	c := valid()
	c.Issuers = []Issuer{goodIssuer()}
	c.Issuer = "https://bus.example:8443"
	c.Registration = "enrollment"
	if err := c.Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

func TestCoreMapsDefaultACL(t *testing.T) {
	c := valid()
	c.DefaultACL = []ACLEntry{{"everyone", []string{"read"}}, {CreatorPlaceholder, []string{"read", "write", "admin"}}}
	cc, err := c.Core()
	if err != nil {
		t.Fatal(err)
	}
	want := []store.ACLEntry{
		{Identity: "everyone", Permissions: []store.Permission{store.PermRead}},
		{Identity: "pat", Permissions: []store.Permission{store.PermRead, store.PermWrite, store.PermAdmin}},
	}
	if got := cc.DefaultACL("pat"); !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultACL(pat) = %v, want %v", got, want)
	}
	if got := cc.DefaultACL(""); len(got) != 1 || got[0].Identity != "everyone" {
		t.Errorf("DefaultACL(\"\") = %v, want only everyone", got)
	}
	cc, err = valid().Core()
	if err != nil {
		t.Fatal(err)
	}
	if cc.DefaultACL != nil {
		t.Error("shipped default must map to a nil DefaultACL")
	}
	// The shipped default and store.DefaultACL agree, so nil is faithful.
	if got := DefaultACLFunc(shippedDefault())("pat"); !reflect.DeepEqual(got, store.DefaultACL("pat")) {
		t.Errorf("shipped literal %v differs from store.DefaultACL %v", got, store.DefaultACL("pat"))
	}
}
