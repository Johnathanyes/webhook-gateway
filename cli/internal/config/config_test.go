package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"webhook-gateway-cli/internal/build"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	want := Config{GatewayURL: "https://gateway.example.com", APIKey: "whg_abc123"}
	path, err := Save(want)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != want {
		t.Errorf("round-tripped config = %+v, want %+v", got, want)
	}

	if base := filepath.Base(filepath.Dir(path)); base != build.Name {
		t.Errorf("config directory = %q, want %q", base, build.Name)
	}
}

// The file holds an API key, so it must not be world- or group-readable.
func TestSavePermissions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	path, err := Save(Config{GatewayURL: "http://localhost:8080", APIKey: "whg_secret"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file mode = %04o, want 0600", perm)
	}

	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("config dir mode = %04o, want 0700", perm)
	}
}

// A missing file means "you haven't logged in", not "your config is corrupt" —
// commands rely on telling those apart.
func TestLoadWithoutConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if _, err := Load(); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("Load() error = %v, want ErrNotLoggedIn", err)
	}
}

func TestDirHonorsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")

	dir, err := Dir()
	if err != nil {
		t.Fatalf("dir: %v", err)
	}
	if want := filepath.Join("/custom/config", build.Name); dir != want {
		t.Errorf("Dir() = %q, want %q", dir, want)
	}
}
