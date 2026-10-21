package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

const envPrefix = "AGENTBUS2_SERVER_"

// EnvName is the environment variable of a setting.
func EnvName(setting string) string { return envPrefix + strings.ToUpper(setting) }

// Overrides maps a setting name to its textual value (the -set form).
type Overrides map[string]string

func unknownSetting(name, source string) error {
	return fmt.Errorf("unknown setting %q from %s; settings: %s", name, source, strings.Join(Settings(), ", "))
}

// Load builds the configuration: defaults, the file when path is not empty
// (a missing file is an error), AGENTBUS2_SERVER_* variables in environ, then
// flags; then Validate.
func Load(path string, environ []string, flags Overrides) (Config, error) {
	c := Default()
	if path != "" {
		if err := loadFile(&c, path); err != nil {
			return c, err
		}
	}
	for _, kv := range environ {
		rest, ok := strings.CutPrefix(kv, envPrefix)
		if !ok {
			continue
		}
		name, text, _ := strings.Cut(rest, "=")
		if name == "" {
			return c, fmt.Errorf("environment variable %q has no setting name", kv)
		}
		if err := set(&c, strings.ToLower(name), text, "environment"); err != nil {
			return c, err
		}
	}
	keys := make([]string, 0, len(flags))
	for k := range flags {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if err := set(&c, k, flags[k], "flag"); err != nil {
			return c, err
		}
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

// loadFile ports v1 internal/config/config.go Load's strict file rules.
func loadFile(c *Config, path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return fmt.Errorf("%s: file is empty, expected a JSON object", path)
	}
	var raw map[string]json.RawMessage
	rawErr := json.Unmarshal(body, &raw)
	// Slices decode into the default's elements; start from empty ones for
	// the keys the file sets.
	if _, ok := raw["issuers"]; ok {
		c.Issuers = nil
	}
	if _, ok := raw["default_acl"]; ok {
		c.DefaultACL = nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(c); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: must contain exactly one JSON object", path)
	}
	if rawErr != nil || raw == nil {
		return fmt.Errorf("%s: must be a JSON object, not null", path)
	}
	for key, v := range raw {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return fmt.Errorf("%s: %q must not be null", path, key)
		}
	}
	return nil
}

// set applies one textual value, by the field's kind; the environment and the
// flags share it.
func set(c *Config, name, text, source string) error {
	_, index := fields()
	i, ok := index[name]
	if !ok {
		return unknownSetting(name, source)
	}
	f := reflect.ValueOf(c).Elem().Field(i)
	bad := func(err error) error {
		return fmt.Errorf("setting %s from %s: invalid value %q: %w", name, source, text, err)
	}
	switch f.Kind() {
	case reflect.String:
		f.SetString(text)
	case reflect.Int:
		n, err := strconv.Atoi(text)
		if err != nil {
			return bad(err)
		}
		f.SetInt(int64(n))
	case reflect.Float64:
		x, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return bad(err)
		}
		f.SetFloat(x)
	case reflect.Bool:
		b, err := strconv.ParseBool(text)
		if err != nil {
			return bad(err)
		}
		f.SetBool(b)
	case reflect.Slice:
		v := reflect.New(f.Type())
		dec := json.NewDecoder(strings.NewReader(text))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v.Interface()); err != nil {
			return bad(err)
		}
		var extra json.RawMessage
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			return bad(errors.New("trailing content after the JSON value"))
		}
		if v.Elem().IsNil() {
			return bad(errors.New("null is not a list"))
		}
		f.Set(v.Elem())
	default:
		return bad(fmt.Errorf("unsupported setting kind %s", f.Kind()))
	}
	return nil
}
