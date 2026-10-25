package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The operator documents under docs/ops are checked against the code, so the
// configuration reference cannot drift from Settings() and Default(). The
// tests are Docker-free: they read files and call Load.

const (
	opsDir    = "../../../docs/ops"
	deployDir = "../../../deploy"
)

var (
	// A backticked environment name in prose or a table. `AGENTBUS2_SERVER_*`
	// (a wildcard) does not match: the star precedes the closing backtick.
	envToken = regexp.MustCompile("`(AGENTBUS2_SERVER_[A-Za-z0-9_]*)`")
	// A settings-table row: | `name` | `ENV` | type | default | ...
	settingRow = regexp.MustCompile("^\\| `([^`]+)` \\| `([^`]+)` \\| ([^|]+?) \\| ([^|]+?) \\|")
	rowName    = regexp.MustCompile("^\\| `([^`]*)`")
	// Any AGENTBUS2_SERVER_ name in the compose file (it is not parsed as YAML).
	composeToken = regexp.MustCompile(`AGENTBUS2_SERVER_[A-Z_]+`)
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func knownEnvNames() map[string]string {
	known := map[string]string{}
	for _, n := range Settings() {
		known[EnvName(n)] = n
	}
	return known
}

// defaultCell is how configuration.md writes a setting's default: ints and
// floats as decimals, strings quoted, an empty string as (unset), and the two
// structured settings as (see below).
func defaultCell(t *testing.T, name string, v reflect.Value) (typ, def string) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		if v.String() == "" {
			return "string", "(unset)"
		}
		return "string", strconv.Quote(v.String())
	case reflect.Int:
		return "int", strconv.FormatInt(v.Int(), 10)
	case reflect.Float64:
		return "float", strconv.FormatFloat(v.Float(), 'f', -1, 64)
	case reflect.Slice:
		if name == "issuers" || name == "default_acl" {
			return "JSON list", "(see below)"
		}
	}
	t.Fatalf("setting %q has kind %s, which the configuration reference has no rule for; extend defaultCell", name, v.Kind())
	return "", ""
}

func TestConfigurationReferenceCoversEverySetting(t *testing.T) {
	doc := readFile(t, filepath.Join(opsDir, "configuration.md"))
	known := knownEnvNames()

	rows := map[string][]string{} // setting name -> the cells of each of its rows
	for _, line := range strings.Split(doc, "\n") {
		m := rowName.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		if !slices.Contains(Settings(), name) {
			t.Errorf("configuration.md has a row for %q, which is not a setting", name)
			continue
		}
		full := settingRow.FindStringSubmatch(line)
		if full == nil {
			t.Errorf("configuration.md row for %q is not `| `name` | `ENV` | type | default | ...`: %s", name, line)
			continue
		}
		rows[name] = append(rows[name], full[2], full[3], full[4])
	}

	d := reflect.ValueOf(Default())
	_, index := fields()
	for _, name := range Settings() {
		cells := rows[name]
		switch {
		case len(cells) == 0:
			t.Errorf("configuration.md has no row for setting %q", name)
			continue
		case len(cells) > 3:
			t.Errorf("configuration.md has %d rows for setting %q, want exactly one", len(cells)/3, name)
			continue
		}
		env, gotType, gotDef := cells[0], strings.TrimSpace(cells[1]), strings.ReplaceAll(strings.TrimSpace(cells[2]), "`", "")
		if env != EnvName(name) {
			t.Errorf("setting %q: environment name %q in the doc, want %q", name, env, EnvName(name))
		}
		wantType, wantDef := defaultCell(t, name, d.Field(index[name]))
		if gotType != wantType {
			t.Errorf("setting %q: type %q in the doc, want %q", name, gotType, wantType)
		}
		if gotDef != wantDef {
			t.Errorf("setting %q: default %q in the doc, want %q", name, gotDef, wantDef)
		}
	}

	files, err := filepath.Glob(filepath.Join(opsDir, "*.md"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no documents under %s: %v", opsDir, err)
	}
	for _, f := range files {
		for _, m := range envToken.FindAllStringSubmatch(readFile(t, f), -1) {
			if _, ok := known[m[1]]; !ok {
				t.Errorf("%s names %s, which is not the environment variable of any setting", filepath.Base(f), m[1])
			}
		}
	}
}

func TestComposeExampleUsesKnownSettings(t *testing.T) {
	known := knownEnvNames()
	compose := readFile(t, filepath.Join(deployDir, "docker-compose.yml"))
	tokens := composeToken.FindAllString(compose, -1)
	if len(tokens) == 0 {
		t.Fatal("docker-compose.yml names no AGENTBUS2_SERVER_ variable; the scan is broken")
	}
	for _, tok := range tokens {
		if _, ok := known[tok]; !ok {
			t.Errorf("docker-compose.yml names %s, which is not the environment variable of any setting", tok)
		}
	}

	path := filepath.Join(deployDir, "config.example.json")
	var members map[string]any
	if err := json.Unmarshal([]byte(readFile(t, path)), &members); err != nil {
		t.Fatalf("config.example.json: %v", err)
	}
	for k := range members {
		if !slices.Contains(Settings(), k) {
			t.Errorf("config.example.json has member %q, which is not a setting", k)
		}
	}
	if _, ok := members["listen"]; !ok {
		t.Error("config.example.json does not set listen; Load would pass without it, the server would not start")
	}
	if _, err := Load(path, nil, nil); err != nil {
		t.Errorf("config.example.json does not load: %v", err)
	}
}

// jsonTags lists the JSON field names of struct type t, descending into struct
// fields (an issuers entry's claims object).
func jsonTags(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		out = append(out, tag)
		if f.Type.Kind() == reflect.Struct {
			out = append(out, jsonTags(f.Type)...)
		}
	}
	return out
}

// TestIssuersRowDocumentsEveryEntryField: the `issuers` row of the
// configuration reference names every field of an issuers entry (and of its
// claims object) in backticks, so a field added to Issuer cannot go
// undocumented.
func TestIssuersRowDocumentsEveryEntryField(t *testing.T) {
	doc := readFile(t, filepath.Join(opsDir, "configuration.md"))
	var row string
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "| `issuers` |") {
			row = line
		}
	}
	if row == "" {
		t.Fatal("configuration.md has no `issuers` row")
	}
	tags := jsonTags(reflect.TypeOf(Issuer{}))
	if len(tags) < 8 {
		t.Fatalf("found only %v; the walk over Issuer is broken", tags)
	}
	for _, tag := range tags {
		if !strings.Contains(row, "`"+tag+"`") {
			t.Errorf("the `issuers` row of configuration.md does not document the entry field %q", tag)
		}
	}
}
