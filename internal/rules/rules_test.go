package rules

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// settings liest einen YAML-Ausschnitt so ein, wie ihn die Konfiguration
// an Build weiterreicht.
func settings(t *testing.T, doc string) map[string]yaml.Node {
	t.Helper()
	if doc == "" {
		return nil
	}
	var parsed map[string]yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatalf("testeinstellungen lesen: %v", err)
	}
	return parsed
}

func build(t *testing.T, general, node string) Set {
	t.Helper()
	set, err := Build(settings(t, general), settings(t, node))
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	return set
}

func mode(t *testing.T, set Set, name string) Mode {
	t.Helper()
	for _, rule := range set {
		if rule.Name() == name {
			return rule.Mode()
		}
	}
	t.Fatalf("regel %q fehlt im satz", name)
	return ModeOff
}

// Ohne Zutun soll sich nichts ändern: Die IO-Begrenzung ist scharf, alles
// Weitere schaltet frei, wer es haben will.
func TestBuildVorgabemodi(t *testing.T) {
	set := build(t, "", "")

	if got := mode(t, set, "io_limits"); got != ModeEnforce {
		t.Errorf("io_limits = %q, erwartet %q", got, ModeEnforce)
	}
	for _, name := range []string{"discard", "ssd", "iothread", "guest_agent", "startup"} {
		if got := mode(t, set, name); got != ModeOff {
			t.Errorf("%s = %q, erwartet %q", name, got, ModeOff)
		}
	}
}

func TestBuildKurzformUndLangform(t *testing.T) {
	set := build(t, "discard: enforce\nssd:\n  mode: report\n  pools: [nvme]\n", "")

	if got := mode(t, set, "discard"); got != ModeEnforce {
		t.Errorf("discard = %q, erwartet %q", got, ModeEnforce)
	}
	if got := mode(t, set, "ssd"); got != ModeReport {
		t.Errorf("ssd = %q, erwartet %q", got, ModeReport)
	}
}

// Wer für einen Node nur den Modus abweichend setzt, soll dessen übrige
// Optionen behalten — sonst müsste man jede Einstellung doppelt pflegen.
func TestBuildNodeUeberschreibtNurGenanntes(t *testing.T) {
	set := build(t,
		"ssd:\n  mode: enforce\n  pools: [nvme]\n",
		"ssd: report\n")

	for _, rule := range set {
		known, ok := rule.(*ssd)
		if !ok {
			continue
		}
		if known.Mode() != ModeReport {
			t.Errorf("modus = %q, erwartet %q", known.Mode(), ModeReport)
		}
		if len(known.Pools) != 1 || known.Pools[0] != "nvme" {
			t.Errorf("pools = %v, erwartet [nvme] aus der allgemeinen einstellung", known.Pools)
		}
	}
}

// YAML kennt "off" seit Version 1.1 als Wahrheitswert. Ohne eigenes
// Einlesen käme hier "false" an und der Dienst bräche beim Start ab.
func TestBuildOffIstKeinWahrheitswert(t *testing.T) {
	set := build(t, "io_limits: off\n", "")

	if got := mode(t, set, "io_limits"); got != ModeOff {
		t.Errorf("io_limits = %q, erwartet %q", got, ModeOff)
	}
}

func TestBuildWeistTippfehlerAb(t *testing.T) {
	tests := []struct {
		name    string
		general string
		node    string
		wantIn  string
	}{
		{"unbekannte regel", "discrad: enforce\n", "", "unbekannte regel"},
		{"unbekannte regel je node", "", "discrad: enforce\n", "unbekannte regel"},
		{"unbekannte option", "ssd:\n  mode: enforce\n  poools: [x]\n", "", "unbekannte option"},
		{"unbekannter modus", "discard: vielleicht\n", "", "unbekannter modus"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build(settings(t, tc.general), settings(t, tc.node))
			if err == nil {
				t.Fatalf("Build() = nil, erwartet einen Fehler")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("Fehler = %v, erwartet %q darin", err, tc.wantIn)
			}
		})
	}
}

// Eine Staffelung ohne Abstand wäre keine. Das soll beim Start auffallen.
func TestStartupBrauchtEinenAbstand(t *testing.T) {
	_, err := Build(settings(t, "startup: enforce\n"), nil)
	if err == nil {
		t.Fatal("Build() = nil, erwartet einen Fehler wegen des fehlenden Abstands")
	}

	if _, err := Build(settings(t, "startup:\n  mode: enforce\n  up: 30s\n"), nil); err != nil {
		t.Errorf("Build() = %v", err)
	}
}

func TestReportOnlySenktScharfeRegelnAb(t *testing.T) {
	set := build(t, "discard: enforce\niothread: report\nssd: off\n", "").ReportOnly()

	if got := mode(t, set, "discard"); got != ModeReport {
		t.Errorf("discard = %q, erwartet %q", got, ModeReport)
	}
	if got := mode(t, set, "ssd"); got != ModeOff {
		t.Errorf("ssd = %q, erwartet %q — ein Probelauf schaltet nichts ein", got, ModeOff)
	}
}
