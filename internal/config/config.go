package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Port          int    `yaml:"port"`
	Role          string `yaml:"role"`           // all | ingest | worker | dashboard
	DatabaseURL   string `yaml:"database_url"`
	AdminPassword string `yaml:"admin_password"`
	EncryptionKey string `yaml:"encryption_key"` // base64, decodes to 32 bytes (AES-256-GCM)
}

var validRoles = map[string]bool{
	"all": true, "ingest": true, "worker": true, "dashboard": true,
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		Port: 8080,  // defaults
		Role: "all",
	}

	// 1. YAML file — optional. Missing file is fine; malformed is not
	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("parsing config file %q: %w", path, err)
			}
		case !os.IsNotExist(err):
			return nil, fmt.Errorf("reading config file %q: %w", path, err)
		}
	}

	// 2. Env overrides — env always wins over the file.
	if v := os.Getenv("PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("PORT must be a number, got %q", v)
		}
		cfg.Port = p
	}
	if v := os.Getenv("ROLE"); v != "" {
		cfg.Role = v
	}
	if v := os.Getenv("DATABASE_URL"); v != "" {
		cfg.DatabaseURL = v
	}
	if v := os.Getenv("ADMIN_PASSWORD"); v != "" {
		cfg.AdminPassword = v
	}
	if v := os.Getenv("ENCRYPTION_KEY"); v != "" {
		cfg.EncryptionKey = v
	}

	// 3. Validate.
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	var problems []string

	if c.DatabaseURL == "" {
		problems = append(problems, "DATABASE_URL is required")
	}
	if c.AdminPassword == "" {
		problems = append(problems, "ADMIN_PASSWORD is required")
	}
	if !validRoles[c.Role] {
		problems = append(problems, fmt.Sprintf("ROLE %q is invalid (want: all, ingest, worker, dashboard)", c.Role))
	}
	if c.Port < 1 || c.Port > 65535 {
		problems = append(problems, fmt.Sprintf("PORT %d is out of range 1-65535", c.Port))
	}

	// The encryption key protects signing secrets at rest (BR-26). AES-256-GCM
	// needs exactly 32 bytes, so we require a base64 string that decodes to 32.
	if c.EncryptionKey == "" {
		problems = append(problems, "ENCRYPTION_KEY is required")
	} else if key, err := base64.StdEncoding.DecodeString(c.EncryptionKey); err != nil {
		problems = append(problems, "ENCRYPTION_KEY must be base64")
	} else if len(key) != 32 {
		problems = append(problems, fmt.Sprintf("ENCRYPTION_KEY must decode to 32 bytes, got %d", len(key)))
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}