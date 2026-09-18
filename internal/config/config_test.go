package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pve-optimizer/internal/mode"
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
	if source != "pools.local-pool" {
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
nodes:
  vmh03:
    pools:
      local-pool:
        mbps_wr: 50
  vmh04:
    defaults:
      mbps_wr: 75
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
		{"vmh03", "local-pool", "nodes.vmh03.pools.local-pool", 50},
		{"vmh02", "local-pool", "pools.local-pool", 200},
		{"vmh04", "sonstwas", "nodes.vmh04.defaults", 75},
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

// Die alte Schreibweise "<node>:<pool>" gibt es nicht mehr. Sie soll beim
// Start auffallen und nicht stillschweigend als Pool namens "vmh03:rpool"
// durchgehen, den es nirgends gibt.
func TestAlteNodeSchreibweiseWirdAbgewiesen(t *testing.T) {
	path := writeConfig(t, `
mode: local
node: vmh03
defaults:
  mbps_wr: 100
pools:
  vmh03:local-pool:
    mbps_wr: 50
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() = nil, erwartet ein Abbrechen wegen der alten Schreibweise")
	}
	if !strings.Contains(err.Error(), "nodes.<node>.pools") {
		t.Errorf("Fehler nennt den neuen Ort nicht: %v", err)
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

// Die mitgelieferte Vorlage muss sich laden lassen — sie ist das, was
// jeder als Erstes kopiert.
func TestBeispielkonfigurationLaedt(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("config.example.yaml: %v", err)
	}

	set, err := cfg.RulesFor("vmh02")
	if err != nil {
		t.Fatalf("RulesFor() = %v", err)
	}
	if len(set) == 0 {
		t.Error("kein regelsatz aus der vorlage")
	}
}

func TestDienstMonitorVorgabeAus(t *testing.T) {
	path := writeConfig(t, "defaults:\n  mbps_rd: 200\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	settings, err := cfg.ServicesFor("vmh03")
	if err != nil {
		t.Fatalf("ServicesFor() = %v", err)
	}
	if settings.Mode != mode.Off {
		t.Errorf("Mode = %q, erwartet %q — was eingreift, ist von Haus aus aus", settings.Mode, mode.Off)
	}
}

func TestDienstMonitorJeNode(t *testing.T) {
	path := writeConfig(t, `
defaults:
  mbps_rd: 200
services:
  mode: report
  restart_limit: 2
  units: [pvestatd.service]
nodes:
  vmh03:
    services:
      mode: enforce
      restart_limit: 5
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	allgemein, err := cfg.ServicesFor("vmh01")
	if err != nil {
		t.Fatalf("ServicesFor(vmh01) = %v", err)
	}
	if allgemein.Mode != mode.Report || allgemein.RestartLimit != 2 {
		t.Errorf("vmh01: Mode = %q, RestartLimit = %d, erwartet report/2", allgemein.Mode, allgemein.RestartLimit)
	}

	abweichend, err := cfg.ServicesFor("vmh03")
	if err != nil {
		t.Fatalf("ServicesFor(vmh03) = %v", err)
	}
	if abweichend.Mode != mode.Enforce || abweichend.RestartLimit != 5 {
		t.Errorf("vmh03: Mode = %q, RestartLimit = %d, erwartet enforce/5", abweichend.Mode, abweichend.RestartLimit)
	}
	// Was der Node nicht nennt, bleibt so, wie es clusterweit steht.
	if len(abweichend.Units) != 1 || abweichend.Units[0] != "pvestatd.service" {
		t.Errorf("vmh03: Units = %v, erwartet die clusterweite Liste", abweichend.Units)
	}
}

func TestDienstMonitorImProbelauf(t *testing.T) {
	path := writeConfig(t, `
dry_run: true
defaults:
  mbps_rd: 200
services:
  mode: enforce
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	settings, err := cfg.ServicesFor("vmh03")
	if err != nil {
		t.Fatalf("ServicesFor() = %v", err)
	}
	if settings.Mode != mode.Report {
		t.Errorf("Mode = %q, erwartet %q — dry_run senkt auch den Monitor ab", settings.Mode, mode.Report)
	}
}

func TestDienstMonitorTippfehlerWirdAbgewiesen(t *testing.T) {
	tests := map[string]string{
		"unbekannter schlüssel":         "services:\n  mode: enforce\n  restart_limt: 3\n",
		"unbekannter schlüssel in mail": "services:\n  mode: enforce\n  mail:\n    empfaenger: a@b.de\n",
		"unsinnige grenze":              "services:\n  mode: enforce\n  restart_limit: 0\n",
		"frist zu kurz":                 "services:\n  mode: enforce\n  stable_after: 30s\n",
	}

	for name, block := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, "defaults:\n  mbps_rd: 200\n"+block)
			if _, err := Load(path); err == nil {
				t.Fatal("Load() nahm die Konfiguration an")
			}
		})
	}
}

func TestSmtpPasswortAusUmgebung(t *testing.T) {
	t.Setenv(smtpPasswordEnv, "geheim")
	path := writeConfig(t, `
defaults:
  mbps_rd: 200
services:
  mode: enforce
  mail:
    to: admin@example.com
    from: pve@example.com
    server: mail.example.com:25
    username: pve
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	settings, err := cfg.ServicesFor("vmh03")
	if err != nil {
		t.Fatalf("ServicesFor() = %v", err)
	}
	if settings.Mail.Password != "geheim" {
		t.Errorf("Password = %q, erwartet den Wert aus der Umgebung", settings.Mail.Password)
	}
}

func TestEmpfehlungenVorgabeAn(t *testing.T) {
	path := writeConfig(t, "defaults:\n  mbps_rd: 200\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	set, err := cfg.AdviceFor("vmh03")
	if err != nil {
		t.Fatalf("AdviceFor() = %v", err)
	}
	if len(set) == 0 {
		t.Fatal("AdviceFor() lieferte keine Prüfungen")
	}
	for _, check := range set {
		if check.Mode() != mode.Report {
			t.Errorf("%s = %q, erwartet %q — Empfehlungen ändern nichts und sind daher an",
				check.Name(), check.Mode(), mode.Report)
		}
	}
}

func TestEmpfehlungenJeNode(t *testing.T) {
	path := writeConfig(t, `
defaults:
  mbps_rd: 200
advice:
  backup_coverage:
    mode: report
    ignore: [101, 3000]
nodes:
  vmh03:
    advice:
      backup_coverage: off
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	modeOf := func(node, name string) mode.Mode {
		t.Helper()
		set, err := cfg.AdviceFor(node)
		if err != nil {
			t.Fatalf("AdviceFor(%s) = %v", node, err)
		}
		for _, check := range set {
			if check.Name() == name {
				return check.Mode()
			}
		}
		t.Fatalf("prüfung %q fehlt", name)
		return ""
	}

	if got := modeOf("vmh01", "backup_coverage"); got != mode.Report {
		t.Errorf("vmh01 = %q, erwartet %q", got, mode.Report)
	}
	if got := modeOf("vmh03", "backup_coverage"); got != mode.Off {
		t.Errorf("vmh03 = %q, erwartet %q vom Node", got, mode.Off)
	}
}

func TestEmpfehlungenTippfehlerWirdAbgewiesen(t *testing.T) {
	tests := map[string]string{
		"unbekannte prüfung": "advice:\n  backup_coverrage: report\n",
		"unbekannte option":  "advice:\n  backup_coverage:\n    ignoore: [1]\n",
		"enforce":            "advice:\n  replication_rate: enforce\n",
	}

	for name, block := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, "defaults:\n  mbps_rd: 200\n"+block)
			if _, err := Load(path); err == nil {
				t.Fatal("Load() nahm die Konfiguration an")
			}
		})
	}
}

// Ein Probelauf schwächt Empfehlungen nicht ab — sie ändern ohnehin nichts.
func TestProbelaufLaesstEmpfehlungenUnberuehrt(t *testing.T) {
	path := writeConfig(t, "dry_run: true\ndefaults:\n  mbps_rd: 200\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	set, err := cfg.AdviceFor("vmh03")
	if err != nil {
		t.Fatalf("AdviceFor() = %v", err)
	}
	for _, check := range set {
		if check.Mode() != mode.Report {
			t.Errorf("%s = %q, erwartet %q", check.Name(), check.Mode(), mode.Report)
		}
	}
}
