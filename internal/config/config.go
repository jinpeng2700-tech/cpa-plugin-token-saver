// Package config owns validation and atomic publication of plugin configuration.
package config

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

const (
	DefaultHeadroomURL       = "http://127.0.0.1:8787"
	DefaultHeadroomTimeoutMS = 1500
	MinHeadroomTimeoutMS     = 100
	MaxHeadroomTimeoutMS     = 1500
	headroomCompressPath     = "/v1/compress"
)

var (
	cavemanLevels = map[string]struct{}{
		"lite": {}, "full": {}, "ultra": {},
		"wenyan-lite": {}, "wenyan": {}, "wenyan-ultra": {},
	}
	ponytailLevels = map[string]struct{}{
		"lite": {}, "full": {}, "ultra": {},
	}
)

// Config is one immutable runtime snapshot. Snapshot returns defensive copies
// of its slice fields, so callers cannot mutate the published value.
type Config struct {
	RTKEnabled        bool
	HeadroomEnabled   bool
	HeadroomURL       string
	HeadroomTimeoutMS int
	CavemanEnabled    bool
	CavemanLevel      string
	PonytailEnabled   bool
	PonytailLevel     string
	ModelAllowlist    []string
	RawYAML           []byte
}

// Defaults keeps the plugin loaded while leaving every token-saving stage off.
func Defaults() Config {
	return Config{
		HeadroomURL:       DefaultHeadroomURL,
		HeadroomTimeoutMS: DefaultHeadroomTimeoutMS,
		CavemanLevel:      "full",
		PonytailLevel:     "full",
		ModelAllowlist:    []string{},
	}
}

// HeadroomEndpoint returns the fixed compression endpoint for the configured
// loopback service. HeadroomURL itself remains a base URL for configuration UI.
func (c Config) HeadroomEndpoint() string {
	return strings.TrimSuffix(c.HeadroomURL, "/") + headroomCompressPath
}

// AllowsModel applies exact, case-sensitive model matching. An empty allowlist
// means every model is eligible.
func (c Config) AllowsModel(model string) bool {
	if len(c.ModelAllowlist) == 0 {
		return true
	}
	for _, allowed := range c.ModelAllowlist {
		if model == allowed {
			return true
		}
	}
	return false
}

// Store publishes validated configurations as atomic immutable snapshots.
type Store struct {
	current atomic.Pointer[Config]
}

// NewStore initializes the store. Invalid cold-start data returns an error but
// leaves a usable safe-off default snapshot installed.
func NewStore(raw []byte) (*Store, error) {
	store := &Store{}
	safe := Defaults()
	store.current.Store(&safe)
	if len(bytes.TrimSpace(raw)) == 0 {
		return store, nil
	}
	parsed, errParse := Parse(raw)
	if errParse != nil {
		return store, errParse
	}
	store.publish(parsed)
	return store, nil
}

// Reload atomically replaces the current snapshot only after full validation.
// A failed hot reload therefore retains the last-known-good configuration.
func (s *Store) Reload(raw []byte) error {
	parsed, errParse := Parse(raw)
	if errParse != nil {
		return errParse
	}
	s.publish(parsed)
	return nil
}

// Snapshot returns a defensive copy of the currently published configuration.
func (s *Store) Snapshot() Config {
	if s == nil {
		return Defaults()
	}
	current := s.current.Load()
	if current == nil {
		return Defaults()
	}
	return clone(*current)
}

func (s *Store) publish(cfg Config) {
	copy := clone(cfg)
	s.current.Store(&copy)
}

func clone(cfg Config) Config {
	cfg.ModelAllowlist = append([]string(nil), cfg.ModelAllowlist...)
	if cfg.ModelAllowlist == nil {
		cfg.ModelAllowlist = []string{}
	}
	cfg.RawYAML = bytes.Clone(cfg.RawYAML)
	return cfg
}

// Parse decodes the flat plugin-owned schema. Host-owned and future fields are
// ignored semantically while the original YAML is retained in RawYAML.
func Parse(raw []byte) (Config, error) {
	cfg := Defaults()
	if len(bytes.TrimSpace(raw)) == 0 {
		return cfg, nil
	}

	root, errDecode := decodeDocument(raw)
	if errDecode != nil {
		return Config{}, errDecode
	}
	if root.Kind != yaml.MappingNode {
		return Config{}, fmt.Errorf("config root must be an object")
	}

	seen := make(map[string]struct{})
	for index := 0; index+1 < len(root.Content); index += 2 {
		keyNode := root.Content[index]
		valueNode := root.Content[index+1]
		if keyNode == nil || keyNode.Kind != yaml.ScalarNode {
			continue
		}
		key := keyNode.Value
		if !isKnownField(key) {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			return Config{}, fmt.Errorf("config field %q is duplicated", key)
		}
		seen[key] = struct{}{}

		switch key {
		case "rtk_enabled":
			value, err := decodeBool(key, valueNode)
			if err != nil {
				return Config{}, err
			}
			cfg.RTKEnabled = value
		case "headroom_enabled":
			value, err := decodeBool(key, valueNode)
			if err != nil {
				return Config{}, err
			}
			cfg.HeadroomEnabled = value
		case "headroom_url":
			value, err := decodeString(key, valueNode)
			if err != nil {
				return Config{}, err
			}
			normalized, errNormalize := normalizeHeadroomURL(value)
			if errNormalize != nil {
				return Config{}, fmt.Errorf("config field %q: %w", key, errNormalize)
			}
			cfg.HeadroomURL = normalized
		case "headroom_timeout_ms":
			value, err := decodeInt(key, valueNode)
			if err != nil {
				return Config{}, err
			}
			if value < MinHeadroomTimeoutMS || value > MaxHeadroomTimeoutMS {
				return Config{}, fmt.Errorf("config field %q must be between %d and %d", key, MinHeadroomTimeoutMS, MaxHeadroomTimeoutMS)
			}
			cfg.HeadroomTimeoutMS = value
		case "caveman_enabled":
			value, err := decodeBool(key, valueNode)
			if err != nil {
				return Config{}, err
			}
			cfg.CavemanEnabled = value
		case "caveman_level":
			value, err := decodeString(key, valueNode)
			if err != nil {
				return Config{}, err
			}
			if _, valid := cavemanLevels[value]; !valid {
				return Config{}, fmt.Errorf("config field %q has unsupported value %q", key, value)
			}
			cfg.CavemanLevel = value
		case "ponytail_enabled":
			value, err := decodeBool(key, valueNode)
			if err != nil {
				return Config{}, err
			}
			cfg.PonytailEnabled = value
		case "ponytail_level":
			value, err := decodeString(key, valueNode)
			if err != nil {
				return Config{}, err
			}
			if _, valid := ponytailLevels[value]; !valid {
				return Config{}, fmt.Errorf("config field %q has unsupported value %q", key, value)
			}
			cfg.PonytailLevel = value
		case "model_allowlist":
			value, err := decodeStringList(key, valueNode)
			if err != nil {
				return Config{}, err
			}
			cfg.ModelAllowlist = value
		}
	}
	cfg.RawYAML = bytes.Clone(raw)
	return cfg, nil
}

func decodeDocument(raw []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if errDecode := decoder.Decode(&document); errDecode != nil {
		return nil, fmt.Errorf("decode config: %w", errDecode)
	}
	if len(document.Content) == 0 {
		return &yaml.Node{Kind: yaml.MappingNode}, nil
	}
	var trailing yaml.Node
	if errTrailing := decoder.Decode(&trailing); errTrailing != io.EOF {
		if errTrailing == nil {
			return nil, fmt.Errorf("config must contain exactly one YAML document")
		}
		return nil, fmt.Errorf("decode config: %w", errTrailing)
	}
	return document.Content[0], nil
}

func isKnownField(key string) bool {
	switch key {
	case "rtk_enabled", "headroom_enabled", "headroom_url", "headroom_timeout_ms",
		"caveman_enabled", "caveman_level", "ponytail_enabled", "ponytail_level",
		"model_allowlist":
		return true
	default:
		return false
	}
}

func decodeBool(field string, node *yaml.Node) (bool, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
		return false, fmt.Errorf("config field %q must be a boolean", field)
	}
	value, errParse := strconv.ParseBool(node.Value)
	if errParse != nil {
		return false, fmt.Errorf("config field %q must be a boolean", field)
	}
	return value, nil
}

func decodeInt(field string, node *yaml.Node) (int, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return 0, fmt.Errorf("config field %q must be an integer", field)
	}
	var value int
	if errDecode := node.Decode(&value); errDecode != nil {
		return 0, fmt.Errorf("config field %q must be an integer", field)
	}
	return value, nil
}

func decodeString(field string, node *yaml.Node) (string, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("config field %q must be a string", field)
	}
	return node.Value, nil
}

func decodeStringList(field string, node *yaml.Node) ([]string, error) {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("config field %q must be an array of strings", field)
	}
	values := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		if item == nil || item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
			return nil, fmt.Errorf("config field %q must contain only strings", field)
		}
		if strings.TrimSpace(item.Value) == "" {
			return nil, fmt.Errorf("config field %q must not contain blank model names", field)
		}
		values = append(values, item.Value)
	}
	return values, nil
}

func normalizeHeadroomURL(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", fmt.Errorf("must be a non-empty URL without surrounding whitespace")
	}
	parsed, errParse := url.Parse(raw)
	if errParse != nil {
		return "", fmt.Errorf("must be a valid URL: %w", errParse)
	}
	if parsed.Scheme != "http" || parsed.Opaque != "" {
		return "", fmt.Errorf("must use the http scheme")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("must not include user information")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", fmt.Errorf("must not include a query")
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("must not include a fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawPath != "" {
		return "", fmt.Errorf("must not configure a path; the plugin uses %s", headroomCompressPath)
	}
	hostname := parsed.Hostname()
	if hostname != "127.0.0.1" && hostname != "::1" {
		return "", fmt.Errorf("host must be the literal loopback address 127.0.0.1 or ::1")
	}
	if port := parsed.Port(); port != "" {
		value, errPort := strconv.Atoi(port)
		if errPort != nil || value < 1 || value > 65535 {
			return "", fmt.Errorf("port must be between 1 and 65535")
		}
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed.String(), nil
}
