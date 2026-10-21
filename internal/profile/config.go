package profile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/ericfitz/agentbus2/internal/repoconfig"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/strictjson"
)

// Mode says where a profile's bus lives.
type Mode string

const (
	// ModeLocal calls the server in process over a SQLite file, with no
	// authentication (Decisions 35, 36).
	ModeLocal Mode = "local"
	// ModeServer talks to a remote server with a bearer token.
	ModeServer Mode = "server"
)

// Profile is one entry of config.json.
type Profile struct {
	Name              string   `json:"-"`
	Mode              Mode     `json:"mode"`
	Server            string   `json:"server,omitempty"`
	Identity          string   `json:"identity,omitempty"`
	CredentialProcess []string `json:"credential_process,omitempty"`
	SQLite            string   `json:"sqlite,omitempty"`
}

// Validate checks the profile's shape. It does not normalize: Load and Save
// strip a trailing slash from Server first.
func (p Profile) Validate() error {
	switch p.Mode {
	case ModeLocal:
		if p.Server != "" {
			return errors.New("server is only valid in server mode")
		}
		if len(p.CredentialProcess) > 0 {
			return errors.New("credential_process is only valid in server mode")
		}
	case ModeServer:
		if err := validServer(p.Server); err != nil {
			return err
		}
		if p.SQLite != "" {
			return errors.New("sqlite is only valid in local mode")
		}
	case "":
		return errors.New(`mode is required ("local" or "server")`)
	default:
		return fmt.Errorf(`mode %q must be "local" or "server"`, string(p.Mode))
	}
	for _, w := range p.CredentialProcess {
		if w == "" {
			return errors.New("credential_process must not contain an empty word")
		}
	}
	if p.Identity != "" {
		if err := store.FileNameRule(p.Identity); err != nil {
			return fmt.Errorf("identity %q: %w", p.Identity, err)
		}
	}
	return nil
}

func validServer(s string) error {
	if s == "" {
		return errors.New("server is required in server mode")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Opaque != "" {
		return fmt.Errorf("server %q must be an absolute http or https URL", s)
	}
	if u.User != nil {
		return fmt.Errorf("server %q must not carry credentials", s)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("server %q must have no path", s)
	}
	if u.RawQuery != "" || u.ForceQuery {
		return fmt.Errorf("server %q must have no query", s)
	}
	if u.Fragment != "" || strings.Contains(s, "#") {
		return fmt.Errorf("server %q must have no fragment", s)
	}
	return nil
}

func (p Profile) normalized() Profile {
	p.Server = strings.TrimSuffix(p.Server, "/")
	return p
}

// Config is the parsed config.json.
type Config struct {
	Path     string             `json:"-"`
	Profiles map[string]Profile `json:"profiles"`
	// TUI holds the terminal UI's settings; nil when config.json has no "tui".
	TUI *TUISettings `json:"tui,omitempty"`
}

// Getenv looks name up in environ with os.Environ semantics (the last
// duplicate wins). An empty value counts as unset.
func Getenv(environ []string, name string) (string, bool) {
	val := ""
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			val = v
		}
	}
	return val, val != ""
}

// DefaultProfile is the built-in profile used when "default" is not defined.
func DefaultProfile() Profile { return Profile{Name: "default", Mode: ModeLocal} }

type configFile struct {
	Profiles map[string]Profile `json:"profiles"`
	TUI      *TUISettings       `json:"tui,omitempty"`
}

// Load reads path strictly: unknown keys, null, trailing content and an empty
// file are errors naming the path. An absent file is an empty profile map.
// Every profile is validated and has its Name set.
func Load(path string) (Config, error) {
	c := Config{Path: path, Profiles: map[string]Profile{}}
	body, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return c, nil
	case err != nil:
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	case len(bytes.TrimSpace(body)) == 0:
		return Config{}, fmt.Errorf("%s: file is empty, expected a JSON object", path)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var f configFile
	if err := dec.Decode(&f); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		return Config{}, fmt.Errorf("%s: must contain exactly one JSON object", path)
	}
	// A top-level null, or "profiles": null, decodes without error.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return Config{}, fmt.Errorf("%s: must be a JSON object, not null", path)
	}
	if v, ok := raw["profiles"]; ok && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		return Config{}, fmt.Errorf(`%s: "profiles" must not be null`, path)
	}
	// The decoder matches member names case-insensitively and lets a
	// repeated member or profile win; both are refused.
	if err := strictjson.Check(body, reflect.TypeFor[configFile]()); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if v, ok := raw["tui"]; ok && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		return Config{}, fmt.Errorf(`%s: "tui" must not be null`, path)
	}
	if f.TUI != nil {
		if err := f.TUI.Validate(); err != nil {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
		c.TUI = f.TUI
	}
	for name, p := range f.Profiles {
		if err := repoconfig.ValidProfileName(name); err != nil {
			return Config{}, fmt.Errorf("%s: profile %q: %w", path, name, err)
		}
		p.Name = name
		p = p.normalized()
		if err := p.Validate(); err != nil {
			return Config{}, fmt.Errorf("%s: profile %q: %w", path, name, err)
		}
		c.Profiles[name] = p
	}
	return c, nil
}

// Save writes the file: the directory 0700, the file 0600, a temp file in the
// same directory synced and renamed into place. Invalid profiles are refused
// before anything is written.
func (c Config) Save() error {
	if c.Path == "" {
		return errors.New("config path is empty")
	}
	out := configFile{Profiles: make(map[string]Profile, len(c.Profiles)), TUI: c.TUI}
	if c.TUI != nil {
		if err := c.TUI.Validate(); err != nil {
			return err
		}
	}
	for name, p := range c.Profiles {
		if err := repoconfig.ValidProfileName(name); err != nil {
			return fmt.Errorf("profile %q: %w", name, err)
		}
		p = p.normalized()
		if err := p.Validate(); err != nil {
			return fmt.Errorf("profile %q: %w", name, err)
		}
		out.Profiles[name] = p
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(c.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.Path)
}

// Resolve returns the named profile. An undefined "default" is the built-in
// local profile; any other undefined name is an error naming the file.
func (c Config) Resolve(name string) (Profile, error) {
	if err := repoconfig.ValidProfileName(name); err != nil {
		return Profile{}, fmt.Errorf("profile %q: %w", name, err)
	}
	if p, ok := c.Profiles[name]; ok {
		p.Name = name
		return p, nil
	}
	if name == "default" {
		return DefaultProfile(), nil
	}
	return Profile{}, fmt.Errorf("profile %q is not defined in %s", name, c.Path)
}

// SQLitePath is the profile's sqlite setting or <dataDir>/agentbus2.sqlite.
func SQLitePath(p Profile, dataDir string) string {
	if p.SQLite != "" {
		return p.SQLite
	}
	return filepath.Join(dataDir, "agentbus2.sqlite")
}

// CredentialDir is the directory holding a profile's credential files.
func CredentialDir(configDir, profileName string) string {
	return filepath.Join(configDir, "credentials", profileName)
}

// TUIColorTheme is one named set of TUI colors. Values are not validated at
// load: the TUI warns about and replaces any it cannot use.
type TUIColorTheme struct {
	Name              string `json:"name"`
	Background        string `json:"background"`
	Text              string `json:"text"`
	Dim               string `json:"dim"`
	Timestamp         string `json:"timestamp"`
	Tag               string `json:"tag"`
	Agent             string `json:"agent"`
	User              string `json:"user"`
	Memory            string `json:"memory"`
	Tasks             string `json:"tasks"`
	Health            string `json:"health"`
	Warn              string `json:"warn"`
	Error             string `json:"error"`
	Selection         string `json:"selection"`
	SelectionInactive string `json:"selection_inactive"`
	SelectionText     string `json:"selection_text"`
	Unsaved           string `json:"unsaved"`
}

// TUISettings is the "tui" object of config.json.
type TUISettings struct {
	Theme     string            `json:"theme,omitempty"`
	Themes    []TUIColorTheme   `json:"themes,omitempty"`
	Icons     string            `json:"icons,omitempty"`
	IconMap   map[string]string `json:"icon_map,omitempty"`
	RailWidth int               `json:"rail_width,omitempty"`
}

// Validate checks the settings' shape. Color values and icon_map names are
// the TUI's to warn about.
func (s TUISettings) Validate() error {
	if s.RailWidth < 0 || s.RailWidth > 1000 {
		return fmt.Errorf("tui.rail_width must be between 0 and 1000, got %d", s.RailWidth)
	}
	switch s.Icons {
	case "", "emoji", "nerdfont", "custom":
	default:
		return fmt.Errorf("tui.icons must be emoji, nerdfont or custom, got %q", s.Icons)
	}
	seen := make(map[string]int, len(s.Themes))
	for i, t := range s.Themes {
		if t.Name == "" {
			return fmt.Errorf("tui.themes[%d].name must not be empty", i)
		}
		if j, dup := seen[t.Name]; dup {
			return fmt.Errorf("tui.themes[%d].name %q repeats tui.themes[%d]", i, t.Name, j)
		}
		seen[t.Name] = i
	}
	if s.Theme != "" && s.Theme != "default" {
		if _, ok := seen[s.Theme]; !ok {
			return fmt.Errorf("tui.theme %q is not default or the name of an entry of tui.themes", s.Theme)
		}
	}
	return nil
}

// SetTUIRailWidth loads the config at path, sets tui.rail_width (creating
// tui) and saves it, so profiles written meanwhile are kept. Load's errors
// are returned and nothing is written; an absent file is created holding
// only tui.
func SetTUIRailWidth(path string, width int) error {
	c, err := Load(path)
	if err != nil {
		return err
	}
	if c.TUI == nil {
		c.TUI = &TUISettings{}
	}
	c.TUI.RailWidth = width
	return c.Save()
}
