package config

import "testing"

func TestLoad_MissingRequiredFails(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("ENCRYPTION_KEY", "")

	_, err := Load("") // no file, no env → should fail
	if err == nil {
		t.Fatal("expected config to fail with missing required fields, got nil")
	}
	t.Logf("got expected error:\n%v", err)
}

func TestLoad_ValidPasses(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/wg")
	t.Setenv("ADMIN_PASSWORD", "hunter2")
	t.Setenv("ENCRYPTION_KEY", "r9y6khV1FnzyliEKfYJJ+J0ylX7+LHSI7UP2kn0c1OI=") // 32 bytes

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
	if cfg.Port != 8080 || cfg.Role != "all" {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}