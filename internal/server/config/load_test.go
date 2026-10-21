package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnvName(t *testing.T) {
	if got := EnvName("token_lifetime_seconds"); got != "AGENTBUS2_SERVER_TOKEN_LIFETIME_SECONDS" {
		t.Errorf("EnvName = %q", got)
	}
}

const (
	issuersA = `[{"issuer":"https://a.example","jwks_uri":"https://a.example/j","audience":"x","algorithms":["ES256"],"subjects":{"s":"pat"}}]`
	issuersB = `[{"issuer":"https://b.example","jwks_uri":"https://b.example/j","audience":"y","algorithms":["EdDSA"],"subjects":{"s":"sam"}}]`
	issuersC = `[{"issuer":"https://c.example","jwks_uri":"https://c.example/j","audience":"z","algorithms":["RS256"],"subjects":{"s":"lee"}}]`
	aclA     = `[{"identity":"everyone","permissions":["read"]}]`
	aclB     = `[{"identity":"admins","permissions":["admin"]}]`
	aclC     = `[{"identity":"everyone","permissions":["write"]}]`
)

func TestPrecedenceTable(t *testing.T) {
	cases := []struct {
		setting, file, env, flag string // file is the JSON literal
		get                      func(Config) string
	}{
		{"listen", `"f:1"`, "e:2", "g:3", func(c Config) string { return c.Listen }},
		{"registration", `"open"`, "enrollment", "closed", func(c Config) string { return c.Registration }},
		{"token_lifetime_seconds", `10`, "20", "30", func(c Config) string { return strconv.Itoa(c.TokenLifetimeSeconds) }},
		{"channel_creation", `"admins"`, "open", "admins", func(c Config) string { return c.ChannelCreation }},
		{"issuers", issuersA, issuersB, issuersC, func(c Config) string { return c.Issuers[0].Audience }},
		{"default_acl", aclA, aclB, aclC, func(c Config) string { return c.DefaultACL[0].Identity + ":" + c.DefaultACL[0].Permissions[0] }},
	}
	for _, tc := range cases {
		t.Run(tc.setting, func(t *testing.T) {
			path := writeFile(t, `{"sqlite":"/x.db","`+tc.setting+`":`+tc.file+`}`)
			env := []string{EnvName(tc.setting) + "=" + tc.env}
			flags := Overrides{tc.setting: tc.flag}
			fileOnly, err := Load(path, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			withEnv, err := Load(path, env, nil)
			if err != nil {
				t.Fatal(err)
			}
			all, err := Load(path, env, flags)
			if err != nil {
				t.Fatal(err)
			}
			fo, we, al := tc.get(fileOnly), tc.get(withEnv), tc.get(all)
			envOnly, err := Load("", env, Overrides{"sqlite": "/x.db"})
			if err != nil {
				t.Fatal(err)
			}
			flagOnly, err := Load("", nil, Overrides{"sqlite": "/x.db", tc.setting: tc.flag})
			if err != nil {
				t.Fatal(err)
			}
			if we != tc.get(envOnly) || al != tc.get(flagOnly) {
				t.Errorf("precedence wrong: env layer %q (want %q), flag layer %q (want %q)", we, tc.get(envOnly), al, tc.get(flagOnly))
			}
			if fo == we || we == al {
				t.Errorf("layers indistinguishable: file %q env %q flag %q", fo, we, al)
			}
		})
	}
	// Concrete values for the scalar layers.
	path := writeFile(t, `{"sqlite":"/x.db","listen":"f:1"}`)
	c, err := Load(path, []string{"AGENTBUS2_SERVER_LISTEN=e:2"}, Overrides{"listen": "g:3"})
	if err != nil || c.Listen != "g:3" {
		t.Errorf("flag layer: %q, %v", c.Listen, err)
	}
	c, err = Load(path, []string{"AGENTBUS2_SERVER_LISTEN=e:2"}, nil)
	if err != nil || c.Listen != "e:2" {
		t.Errorf("env layer: %q, %v", c.Listen, err)
	}
	c, err = Load(path, nil, nil)
	if err != nil || c.Listen != "f:1" {
		t.Errorf("file layer: %q, %v", c.Listen, err)
	}
}

func TestLoadFileRules(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json"), nil, Overrides{"sqlite": "x"}); err == nil {
		t.Error("a missing named file accepted")
	}
	c, err := Load("", nil, Overrides{"sqlite": "x"})
	if err != nil || c.TokenLifetimeSeconds != 3600 {
		t.Errorf("path \"\" should give defaults: %+v, %v", c, err)
	}
	cases := []struct{ name, body, want string }{
		{"empty", "  \n", "file is empty, expected a JSON object"},
		{"unknown key", `{"sqlite":"x","colour":"red"}`, "colour"},
		{"null value", `{"sqlite":"x","listen":null}`, `"listen" must not be null`},
		{"two objects", `{"sqlite":"x"} {"sqlite":"y"}`, "must contain exactly one JSON object"},
		{"junk after", `{"sqlite":"x"} junk`, "must contain exactly one JSON object"},
		{"array", `[1]`, "array"},
		{"null file", `null`, "must be a JSON object, not null"},
		{"bound", `{"sqlite":"x","max_message_kib":0}`, "max_message_kib must be between 1 and 1024, got 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeFile(t, tc.body), nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load = %v, want error containing %q", err, tc.want)
			}
		})
	}
	// A file's list replaces the default rather than merging into it.
	c, err = Load(writeFile(t, `{"sqlite":"x","default_acl":`+aclA+`}`), nil, nil)
	if err != nil || len(c.DefaultACL) != 1 || len(c.DefaultACL[0].Permissions) != 1 {
		t.Errorf("default_acl from file: %+v, %v", c.DefaultACL, err)
	}
}

func TestRejectsUnknownAndMalformed(t *testing.T) {
	base := Overrides{"sqlite": "x"}
	_, err := Load("", []string{"AGENTBUS2_SERVER_COLOUR=1"}, base)
	if err == nil || !strings.Contains(err.Error(), "colour") || !strings.Contains(err.Error(), "token_lifetime_seconds") {
		t.Errorf("unknown env: %v", err)
	}
	_, err = Load("", nil, Overrides{"sqlite": "x", "colour": "1"})
	if err == nil || !strings.Contains(err.Error(), "colour") || !strings.Contains(err.Error(), "flag") {
		t.Errorf("unknown flag: %v", err)
	}
	_, err = Load("", []string{"AGENTBUS2_SERVER_TOKEN_LIFETIME_SECONDS=soon"}, base)
	if err == nil || !strings.Contains(err.Error(), "token_lifetime_seconds") || !strings.Contains(err.Error(), "environment") || !strings.Contains(err.Error(), `"soon"`) {
		t.Errorf("malformed int: %v", err)
	}
	_, err = Load("", []string{`AGENTBUS2_SERVER_DEFAULT_ACL=[{"identity":"everyone","permissions":[]}]`}, base)
	if err == nil || !strings.Contains(err.Error(), "default_acl") || !strings.Contains(err.Error(), "entry 0") {
		t.Errorf("empty permissions: %v", err)
	}
	_, err = Load("", []string{"AGENTBUS2_SERVER_ISSUERS=[] x"}, base)
	if err == nil {
		t.Error("trailing content in JSON text accepted")
	}
	_, err = Load("", []string{"AGENTBUS2_SERVER_ISSUERS=null"}, base)
	if err == nil {
		t.Error("null list accepted")
	}
	_, err = Load("", []string{"AGENTBUS2_SERVER_ISSUERS=" + `[{"colour":1}]`}, base)
	if err == nil {
		t.Error("unknown key inside issuers accepted")
	}
	_, err = Load("", []string{"AGENTBUS2_SERVER_=x"}, base)
	if err == nil {
		t.Error("prefix without a name accepted")
	}
	c, err := Load("", []string{"HOME=/x", "AGENTBUS2_OTHER=1", "AGENTBUS2_SERVER_LISTEN=a:1"}, base)
	if err != nil || c.Listen != "a:1" {
		t.Errorf("unrelated variables should be ignored: %v", err)
	}
	// Embedding timeout is a float.
	c, err = Load("", nil, Overrides{"sqlite": "x", "embedding_query_timeout_seconds": "2.5"})
	if err != nil || c.EmbeddingQueryTimeoutSeconds != 2.5 {
		t.Errorf("float setting: %v %v", c.EmbeddingQueryTimeoutSeconds, err)
	}
}
