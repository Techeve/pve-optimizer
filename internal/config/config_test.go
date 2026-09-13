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

	profile, source := cfg.ProfileFor("vmh02", "local-pool")
	if source != "local-pool" {
		t.Fatalf("ProfileFor(local-pool) = %q, erwartet das pool-profil", source)
	}
	if profile["mbps_rd"] != 500 {
		t.Errorf("mbps_rd = %d, erwartet 500", profile["mbps_rd"])
	}

	fallback, source := cfg.ProfileFor("vmh02", "unbekannter-pool")
	if source != "defaults" {
		t.Errorf("ProfileFor(unbekannter-pool) = %q, erwartet \"defaults\"", source)
	}
	if fallback["mbps_rd"] != 200 {
		t.Errorf("Vorgabe mbps_rd = %d, erwartet 200", fallback["mbps_rd"])
	}
}

// Gleichnamige Pools liegen auf verschiedenen Nodes auf unterschiedlicher
// Hardware — ein Profil je Node muss das Pool-Profil schlagen.
func TestProfileForNodeSchlaegtPool(t *testing.T) {
	path := writeConfig(t, `
mode: local
node: vmh03
defaults:
  mbps_wr: 100
pools:
  local-pool:
    mbps_wr: 200
  vmh03:local-pool:
    mbps_wr: 50
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	tests := []struct {
		node, pool string
		wantSource string
		wantValue  int
	}{
		{"vmh03", "local-pool", "vmh03:local-pool", 50},
		{"vmh02", "local-pool", "local-pool", 200},
		{"vmh02", "sonstwas", "defaults", 100},
	}

	for _, tc := range tests {
		profile, source := cfg.ProfileFor(tc.node, tc.pool)
		if source != tc.wantSource {
			t.Errorf("ProfileFor(%q, %q) = %q, erwartet %q", tc.node, tc.pool, source, tc.wantSource)
		}
		if profile["mbps_wr"] != tc.wantValue {
			t.Errorf("ProfileFor(%q, %q) mbps_wr = %d, erwartet %d",
				tc.node, tc.pool, profile["mbps_wr"], tc.wantValue)
		}
	}
}

func TestOnlyOwnNodeVorgabe(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"local beschränkt sich von selbst", "mode: local\nnode: vmh02\ndefaults:\n  mbps_rd: 1\n", true},
		{"api sieht den ganzen cluster", "mode: api\napi:\n  url: https://x:8006\n  token_id: a!b\n  token_secret: c\ndefaults:\n  mbps_rd: 1\n", false},
		{"ausdrücklich abgeschaltet", "mode: local\nonly_own_node: false\ndefaults:\n  mbps_rd: 1\n", false},
		{"ausdrücklich eingeschaltet", "mode: api\nonly_own_node: true\nnode: vmh02\napi:\n  url: https://x:8006\n  token_id: a!b\n  token_secret: c\ndefaults:\n  mbps_rd: 1\n", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tc.content))
			if err != nil {
				t.Fatalf("Load() = %v", err)
			}
			if got := cfg.RestrictedToOwnNode(); got != tc.want {
				t.Errorf("RestrictedToOwnNode() = %v, erwartet %v", got, tc.want)
			}
			if tc.want && cfg.Node == "" {
				t.Error("Node muss gefüllt sein, wenn der Dienst sich beschränkt")
			}
		})
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
