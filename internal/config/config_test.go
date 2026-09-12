package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("testkonfiguration schreiben: %v", err)
	}
	return path
}

func TestLoadVorgabewerte(t *testing.T) {
	path := writeConfig(t, `
defaults:
  mbps_rd: 200
  mbps_wr: 150
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.Mode != ModeLocal {
		t.Errorf("Mode = %q, erwartet %q", cfg.Mode, ModeLocal)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %s, erwartet 30s", cfg.PollInterval)
	}
	if cfg.StateFile == "" {
		t.Error("StateFile sollte einen Vorgabewert haben")
	}
}

func TestLoadPoolProfile(t *testing.T) {
	path := writeConfig(t, `
mode: local
defaults:
  mbps_rd: 200
pools:
  local-pool:
    mbps_rd: 500
    iops_rd: 20000
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	profile, found := cfg.ProfileFor("local-pool")
	if !found {
		t.Fatal("ProfileFor(local-pool) sollte ein eigenes Profil finden")
	}
	if profile["mbps_rd"] != 500 {
		t.Errorf("mbps_rd = %d, erwartet 500", profile["mbps_rd"])
	}

	fallback, found := cfg.ProfileFor("unbekannter-pool")
	if found {
		t.Error("ProfileFor(unbekannter-pool) darf kein eigenes Profil melden")
	}
	if fallback["mbps_rd"] != 200 {
		t.Errorf("Vorgabe mbps_rd = %d, erwartet 200", fallback["mbps_rd"])
	}
}

func TestLoadFehler(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"unbekannter modus", "mode: irgendwas\ndefaults:\n  mbps_rd: 1\n"},
		{"defaults fehlen", "mode: local\n"},
		{"tippfehler im schluessel", "mode: local\ndefaults:\n  mpbs_rd: 200\n"},
		{"negativer wert", "mode: local\ndefaults:\n  mbps_rd: -5\n"},
		{"zu kurzes intervall", "mode: local\npoll_interval: 100ms\ndefaults:\n  mbps_rd: 1\n"},
		{"api ohne url", "mode: api\ndefaults:\n  mbps_rd: 1\n"},
		{"unbekannter pool-schluessel", "mode: local\ndefaults:\n  mbps_rd: 1\npools:\n  x:\n    quatsch: 1\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, tc.content)); err == nil {
				t.Error("Load() sollte einen Fehler liefern")
			}
		})
	}
}

func TestTokenSecretAusUmgebung(t *testing.T) {
	path := writeConfig(t, `
mode: api
api:
  url: https://pve.example:8006
  token_id: root@pam!optimizer
defaults:
  mbps_rd: 200
`)

	t.Setenv(tokenSecretEnv, "geheim")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.API.TokenSecret != "geheim" {
		t.Errorf("TokenSecret = %q, erwartet aus der Umgebung", cfg.API.TokenSecret)
	}
}
