// Package config reads and writes the CLI's on-disk credentials.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"webhook-gateway-cli/internal/build"
)

// Config is the whole file. It holds one gateway, not a set of named profiles:
// a developer points the CLI at their gateway once and forgets about it.
type Config struct {
	GatewayURL string `yaml:"gateway_url"`
	APIKey     string `yaml:"api_key"`
}

// ErrNotLoggedIn is returned by Load when no config file exists yet, so
// commands can tell "you haven't logged in" apart from "your config is broken".
var ErrNotLoggedIn = errors.New("not logged in")

// Dir is the config directory: $XDG_CONFIG_HOME/<name>, or ~/.config/<name>.
// os.UserConfigDir is deliberately not used — on macOS it resolves to
// ~/Library/Application Support, and a CLI's config belongs in ~/.config where
// developers expect to find it.
func Dir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, build.Name), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".config", build.Name), nil
}

// Path is the config file itself.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// Load reads the stored credentials, returning ErrNotLoggedIn if none exist.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, ErrNotLoggedIn
		}
		return Config{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes the credentials, creating the directory if needed. The file holds
// an API key, so it is written 0600 and its directory 0700.
func Save(cfg Config) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("encoding config: %w", err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}
