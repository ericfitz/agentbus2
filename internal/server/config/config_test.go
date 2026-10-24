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
		Name: "corp", Issuer: "https://idp.example", JWKSURI: "https://idp.example/jwks", Audience: "agentbus",
		Algorithms: []string{"ES256"},
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
		{"no name", func(c *Config) { i := goodIssuer(); i.Name = ""; c.Issuers = []Issuer{i} }, "name is required"},
		{"name slash", func(c *Config) { i := goodIssuer(); i.Name = "a/b"; c.Issuers = []Issuer{i} }, "name"},
		{"name everyone", func(c *Config) { i := goodIssuer(); i.Name = "everyone"; c.Issuers = []Issuer{i} }, "reserved"},
		{"duplicate name", func(c *Config) {
			i, j := goodIssuer(), goodIssuer()
			j.Issuer = "https://other.example"
			c.Issuers = []Issuer{i, j}
		}, `issuers: entry 1: name "corp" repeats entry 0`},
		{"lifetime negative", func(c *Config) { i := goodIssuer(); i.HumanTokenLifetimeSeconds = -1; c.Issuers = []Issuer{i} }, "human_token_lifetime_seconds"},
		{"lifetime too long", func(c *Config) { i := goodIssuer(); i.HumanTokenLifetimeSeconds = 86401; c.Issuers = []Issuer{i} }, "human_token_lifetime_seconds"},
		{"jwks uri", func(c *Config) { i := goodIssuer(); i.JWKSURI = "jwks"; c.Issuers = []Issuer{i} }, "jwks_uri"},
		{"duplicate issuer", func(c *Config) {
			i, j := goodIssuer(), goodIssuer()
			j.Audience = "other"
			c.Issuers = []Issuer{i, j}
		}, `issuers: entry 1: issuer "https://idp.example" repeats entry 0`},
		{"own issuer", func(c *Config) {
			c.Issuer = "https://idp.example"
			c.Issuers = []Issuer{goodIssuer()}
		}, `issuers: entry 0: issuer "https://idp.example" is the server's own issuer`},
		{"channel creation", func(c *Config) { c.ChannelCreation = "closed" }, "channel_creation"},
		{"log level", func(c *Config) { c.LogLevel = "trace" }, "log_level"},
		{"embedding no model", func(c *Config) { c.EmbeddingEndpoint = "http://e" }, "embedding_model"},
		{"embedding relative", func(c *Config) { c.EmbeddingEndpoint = "e"; c.EmbeddingModel = "m" }, "embedding_endpoint"},
		{"both keys", func(c *Config) { c.EmbeddingAPIKeyFile = "f"; c.EmbeddingAPIKeyEnv = "E" }, "embedding_api_key"},
		{"receive counts", func(c *Config) { c.ReceiveDefaultCount = 20; c.ReceiveMaxCount = 10 }, "receive_default_count"},
		{"core bound", func(c *Config) { c.MaxMessageKiB = 0 }, "max_message_kib"},
		{"budget bound", func(c *Config) { c.SQLiteBudgetMiB = 1 }, "sqlite_budget_mib"},
		{"cleanup bound", func(c *Config) { c.CleanupFreePercent = 95 }, "cleanup_free_percent"},
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

// TestWarnings: a plain-http jwks_uri on a non-loopback host is a warning
// (the spec states no transport rule for it, so it is not refused).
func TestWarnings(t *testing.T) {
	cases := []struct {
		uri  string
		warn bool
	}{
		{"https://idp.example/jwks", false},
		{"http://idp.example/jwks", true},
		{"HTTP://idp.example/jwks", true},
		{"http://10.0.0.5:8080/jwks", true},
		{"http://127.0.0.1:8080/jwks", false},
		{"http://[::1]/jwks", false},
		{"http://localhost/jwks", false},
		{"http://LocalHost:9/jwks", false},
	}
	for _, tc := range cases {
		c := valid()
		i := goodIssuer()
		i.JWKSURI = tc.uri
		c.Issuers = []Issuer{goodIssuer(), i}
		c.Issuers[0].Issuer = "https://first.example"
		w := c.Warnings()
		if !tc.warn {
			if len(w) != 0 {
				t.Errorf("%s: unexpected warnings %q", tc.uri, w)
			}
			continue
		}
		if len(w) != 1 || !strings.Contains(w[0], "issuers: entry 1") || !strings.Contains(w[0], tc.uri) {
			t.Errorf("%s: warnings = %q, want one naming entry 1 and the URI", tc.uri, w)
		}
	}
	if w := valid().Warnings(); len(w) != 0 {
		t.Errorf("default config warns: %q", w)
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

// TestIssuerValidation: the entry name and the human lifetime (spec 5.1).
func TestIssuerValidation(t *testing.T) {
	for _, n := range []int{0, 1, 900, 86400} {
		c := valid()
		i := goodIssuer()
		i.HumanTokenLifetimeSeconds = n
		c.Issuers = []Issuer{i}
		if err := c.Validate(); err != nil {
			t.Errorf("lifetime %d rejected: %v", n, err)
		}
	}
	c := valid()
	i := goodIssuer()
	i.Claims = IssuerClaims{Email: "mail", Groups: "roles", Subject: "user_id"}
	c.Issuers = []Issuer{i}
	if err := c.Validate(); err != nil {
		t.Errorf("custom claims rejected: %v", err)
	}
}
