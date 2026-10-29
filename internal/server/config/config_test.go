package config

import (
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/signingkeys"
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
	if d.ScopeIdleHours != 336 {
		t.Errorf("scope_idle_hours default = %d, want 336", d.ScopeIdleHours)
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
		{"postgres budget bound", func(c *Config) { c.PostgresBudgetMiB = 1 }, "postgres_budget_mib"},
		{"inspection on failure", func(c *Config) { c.InspectionOnFailure = "maybe" }, "inspection_on_failure"},
		{"inspection timeout NaN", func(c *Config) { c.InspectionTimeoutSeconds = math.NaN() }, "inspection_timeout_seconds"},
		{"inspection timeout Inf", func(c *Config) { c.InspectionTimeoutSeconds = math.Inf(1) }, "inspection_timeout_seconds"},
		{"embedding timeout NaN", func(c *Config) { c.EmbeddingQueryTimeoutSeconds = math.NaN() }, "embedding_query_timeout_seconds"},
		{"embedding timeout -Inf", func(c *Config) { c.EmbeddingQueryTimeoutSeconds = math.Inf(-1) }, "embedding_query_timeout_seconds"},
		{"inspection timeout", func(c *Config) { c.InspectionTimeoutSeconds = 0 }, "inspection_timeout_seconds"},
		{"idle window bound", func(c *Config) { c.IdleWindowHours = 71 }, "idle_window_hours"},
		{"registration", func(c *Config) { c.Registration = "tofu" }, "registration"},
		{"registration closed is gone", func(c *Config) { c.Registration = "closed" }, `registration must be open or enrollment, got "closed"`},
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
		{"name humans", func(c *Config) { i := goodIssuer(); i.Name = "humans"; c.Issuers = []Issuer{i} }, "reserved"},
		{"name slack-bridge", func(c *Config) { i := goodIssuer(); i.Name = "slack-bridge"; c.Issuers = []Issuer{i} }, "reserved"},
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
		{"scope idle low", func(c *Config) { c.ScopeIdleHours = 0 }, "scope_idle_hours"},
		{"scope idle high", func(c *Config) { c.ScopeIdleHours = 8761 }, "scope_idle_hours"},
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

func TestSlackSettings(t *testing.T) {
	d := Default()
	if d.SlackArchiveAfterDoneHours != 168 || d.SlackMaxMessageChars != 3000 || d.SlackAppTokenFile != "" || d.SlackBotTokenFile != "" {
		t.Errorf("Slack defaults = %+v", d)
	}
	cases := []struct {
		name string
		mut  func(*Config)
		want string // "" accepted
	}{
		{"neither file", func(*Config) {}, ""},
		{"both files", func(c *Config) { c.SlackAppTokenFile, c.SlackBotTokenFile = "/a", "/b" }, ""},
		{"app file alone", func(c *Config) { c.SlackAppTokenFile = "/a" }, "slack_app_token_file and slack_bot_token_file must be set together"},
		{"bot file alone", func(c *Config) { c.SlackBotTokenFile = "/b" }, "slack_app_token_file and slack_bot_token_file must be set together"},
		{"hours 0", func(c *Config) { c.SlackArchiveAfterDoneHours = 0 }, "slack_archive_after_done_hours"},
		{"hours 1", func(c *Config) { c.SlackArchiveAfterDoneHours = 1 }, ""},
		{"hours 8760", func(c *Config) { c.SlackArchiveAfterDoneHours = 8760 }, ""},
		{"hours 8761", func(c *Config) { c.SlackArchiveAfterDoneHours = 8761 }, "slack_archive_after_done_hours"},
		{"chars 199", func(c *Config) { c.SlackMaxMessageChars = 199 }, "slack_max_message_chars"},
		{"chars 200", func(c *Config) { c.SlackMaxMessageChars = 200 }, ""},
		{"chars 3000", func(c *Config) { c.SlackMaxMessageChars = 3000 }, ""},
		{"chars 3001", func(c *Config) { c.SlackMaxMessageChars = 3001 }, "slack_max_message_chars"},
	}
	for _, tc := range cases {
		c := valid()
		tc.mut(&c)
		err := c.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

// open note: signing-keys 6
func TestSigningKeyDefaults(t *testing.T) {
	d := Default()
	if d.SigningKeyURI != "" || d.SigningKeyRotationHours != 720 || d.SigningKeyRotationCooldownSeconds != 3600 {
		t.Errorf("signing key defaults: %q %d %d", d.SigningKeyURI, d.SigningKeyRotationHours, d.SigningKeyRotationCooldownSeconds)
	}
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSigningKeyURIValidation(t *testing.T) {
	for _, uri := range []string{"", "awskms://x", "gcpkms://x", "azurekms://x", "hashivault://x"} {
		c := valid()
		c.SigningKeyURI = uri
		if err := c.Validate(); err != nil {
			t.Errorf("signing_key_uri %q: %v", uri, err)
		}
	}
	for _, uri := range []string{"gcs://x", "awskms", "AWSKMS://x", "awskms://", "://x"} {
		c := valid()
		c.SigningKeyURI = uri
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "signing_key_uri") {
			t.Errorf("signing_key_uri %q: err %v, want a signing_key_uri refusal", uri, err)
			continue
		}
		if uri != "awskms://" {
			for _, s := range []string{"awskms", "gcpkms", "azurekms", "hashivault"} {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("signing_key_uri %q: %q does not name %s", uri, err, s)
				}
			}
		}
	}
}

func TestSigningKeyRotationBounds(t *testing.T) {
	for _, h := range []int{0, 48, 720, 8760} {
		c := valid()
		c.SigningKeyRotationHours = h
		if err := c.Validate(); err != nil {
			t.Errorf("signing_key_rotation_hours %d: %v", h, err)
		}
	}
	for _, h := range []int{1, 47, 8761, -1} {
		c := valid()
		c.SigningKeyRotationHours = h
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "signing_key_rotation_hours") {
			t.Errorf("signing_key_rotation_hours %d: err %v", h, err)
		}
	}
	for _, s := range []int{1, 3600, 86400} {
		c := valid()
		c.SigningKeyRotationCooldownSeconds = s
		if err := c.Validate(); err != nil {
			t.Errorf("signing_key_rotation_cooldown_seconds %d: %v", s, err)
		}
	}
	for _, s := range []int{0, -1, 86401} {
		c := valid()
		c.SigningKeyRotationCooldownSeconds = s
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "signing_key_rotation_cooldown_seconds") {
			t.Errorf("signing_key_rotation_cooldown_seconds %d: err %v", s, err)
		}
	}
}

// The removal time of a retiring key assumes no bearer lives longer than
// MaxBearerLifetime, so it must be the upper bound both lifetimes validate to.
func TestMaxBearerLifetimeIsTheCeiling(t *testing.T) {
	if got := time.Duration(MaxTokenLifetimeSeconds) * time.Second; got != signingkeys.MaxBearerLifetime {
		t.Fatalf("MaxTokenLifetimeSeconds is %v, signingkeys.MaxBearerLifetime %v", got, signingkeys.MaxBearerLifetime)
	}
	c := valid()
	c.TokenLifetimeSeconds = MaxTokenLifetimeSeconds + 1
	if err := c.Validate(); err == nil {
		t.Error("token_lifetime_seconds above the ceiling was accepted")
	}
	c = valid()
	c.TokenLifetimeSeconds = MaxTokenLifetimeSeconds
	if err := c.Validate(); err != nil {
		t.Error(err)
	}
	for _, v := range []int{MaxTokenLifetimeSeconds, MaxTokenLifetimeSeconds + 1} {
		c = valid()
		c.Issuers = []Issuer{goodIssuer()}
		c.Issuers[0].HumanTokenLifetimeSeconds = v
		err := c.Validate()
		if (v > MaxTokenLifetimeSeconds) != (err != nil) {
			t.Errorf("human_token_lifetime_seconds %d: err %v", v, err)
		}
	}
}

// The inspection endpoint follows the profile oidc.issuer rule (spec 4.3): an
// absolute https URL, or http on a loopback host; no credentials in the URL.
func TestInspectionSettings(t *testing.T) {
	const rule = "inspection_endpoint must be https, or http on a loopback host"
	ok := []string{
		"https://inspector.example/v1",
		"https://inspector.example:8443/v1?tenant=a",
		"HTTPS://inspector.example/v1",
		"http://127.0.0.1:9/x",
		"http://127.1.2.3:9/x",
		"http://[::1]:9/x",
		"http://localhost:9/x",
		"http://LOCALHOST:9/x",
	}
	for _, ep := range ok {
		c := valid()
		c.InspectionEndpoint = ep
		if err := c.Validate(); err != nil {
			t.Errorf("endpoint %q: %v, want accepted", ep, err)
		}
	}
	bad := map[string]string{
		"http://inspector.example/x":          rule,
		"http://10.0.0.5:9/x":                 rule,
		"http://localhost.example/x":          rule,
		"ftp://x/y":                           rule,
		"file:///etc/passwd":                  rule,
		"inspector":                           rule,
		"/v1/inspect":                         rule,
		"https:///v1":                         rule,
		"https://":                            rule,
		"http://:9/x":                         rule,
		"https://%zz/x":                       rule,
		"mailto:a@b.example":                  rule,
		"https:inspector.example":             rule,
		"https://user:pw@inspector.example/x": "must not carry credentials",
		"https://user@inspector.example/x":    "must not carry credentials",
		"http://user:pw@127.0.0.1:9/x":        "must not carry credentials",
	}
	for ep, want := range bad {
		c := valid()
		c.InspectionEndpoint = ep
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("endpoint %q: Validate() = %v, want error containing %q", ep, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "pw@") {
			t.Errorf("endpoint %q: the error echoes the password: %v", ep, err)
		}
	}

	both := valid()
	both.InspectionEndpoint = "https://inspector.example/v1"
	both.InspectionAPIKeyFile, both.InspectionAPIKeyEnv = "f", "E"
	if err := both.Validate(); err == nil || !strings.Contains(err.Error(), "at most one of inspection_api_key_file and inspection_api_key_env") {
		t.Errorf("both key settings: %v", err)
	}
	for name, mut := range map[string]func(*Config){
		"file": func(c *Config) { c.InspectionAPIKeyFile = "f" },
		"env":  func(c *Config) { c.InspectionAPIKeyEnv = "E" },
	} {
		c := valid()
		mut(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "inspection_api_key_file and inspection_api_key_env need inspection_endpoint") {
			t.Errorf("key %s without endpoint: %v", name, err)
		}
	}
	for name, mut := range map[string]func(*Config){
		"file": func(c *Config) { c.InspectionAPIKeyFile = "f" },
		"env":  func(c *Config) { c.InspectionAPIKeyEnv = "E" },
	} {
		c := valid()
		c.InspectionEndpoint = "https://inspector.example/v1"
		mut(&c)
		if err := c.Validate(); err != nil {
			t.Errorf("key %s with endpoint: %v", name, err)
		}
	}
	if d := Default(); d.InspectionEndpoint != "" || d.InspectionAPIKeyFile != "" || d.InspectionAPIKeyEnv != "" {
		t.Errorf("defaults are not unset: %+v", d)
	}
}

// A rejected URL setting never echoes the path, the query string or userinfo.
func TestURLErrorsNameSchemeAndHostOnly(t *testing.T) {
	const secret = "SECRETQ"
	cases := []struct {
		name string
		mut  func(*Config, string)
	}{
		{"inspection", func(c *Config, u string) { c.InspectionEndpoint = u }},
		{"embedding", func(c *Config, u string) { c.EmbeddingEndpoint = u; c.EmbeddingModel = "m" }},
	}
	urls := []string{
		"http://inspector.example/path/SECRETPATH?token=" + secret,
		"ftp://inspector.example/path/SECRETPATH?token=" + secret,
		"https://%zz/SECRETPATH?token=" + secret,
		"/SECRETPATH?token=" + secret,
	}
	for _, tc := range cases {
		for _, u := range urls {
			c := valid()
			tc.mut(&c, u)
			err := c.Validate()
			if tc.name == "embedding" && strings.HasPrefix(u, "http://") {
				continue // an absolute http URL is valid for embeddings
			}
			if tc.name == "embedding" && strings.HasPrefix(u, "ftp://") {
				continue
			}
			if err == nil {
				t.Errorf("%s %q: accepted", tc.name, u)
				continue
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "SECRETPATH") {
				t.Errorf("%s %q: error echoes the query or path: %v", tc.name, u, err)
			}
		}
	}
	c := valid()
	c.InspectionEndpoint = "http://inspector.example/p?token=" + secret
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "http://inspector.example") {
		t.Errorf("error should name scheme and host: %v", err)
	}
	c = valid()
	c.Issuer = "http://x/SECRETPATH?token=" + secret
	if err := c.Validate(); err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "SECRETPATH") {
		t.Errorf("issuer error: %v", err)
	}
}
