package services

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pve-optimizer/internal/mode"
)

const unit = "pvestatd.service"

// jetzt ist ein fester Zeitpunkt — die Tests rechnen mit Laufzeiten, und
// die echte Uhr würde sie wackelig machen.
var jetzt = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

type fakeSystemd struct {
	status   status
	restarts int
	fail     error
}

func (f *fakeSystemd) Status(context.Context, string) (status, error) {
	return f.status, nil
}

func (f *fakeSystemd) Restart(context.Context, string) error {
	f.restarts++
	return f.fail
}

type fakeSender struct {
	subjects []string
	bodies   []string
	fail     error
}

func (f *fakeSender) Send(subject, body string) error {
	if f.fail != nil {
		return f.fail
	}
	f.subjects = append(f.subjects, subject)
	f.bodies = append(f.bodies, body)
	return nil
}

func (f *fakeSender) Describe() string { return "test" }

func testMonitor(t *testing.T, sd *fakeSystemd, sender *fakeSender) *Monitor {
	t.Helper()

	settings := DefaultSettings()
	settings.Mode = mode.Enforce
	settings.RestartLimit = 3

	loaded, err := loadState(filepath.Join(t.TempDir(), "services.json"))
	if err != nil {
		t.Fatalf("zählerstand: %v", err)
	}
	return &Monitor{
		settings: settings,
		node:     "vmh03",
		systemd:  sd,
		mail:     sender,
		state:    loaded,
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:      func() time.Time { return jetzt },
	}
}

func TestAbgestuerzterDienstWirdNeuGestartet(t *testing.T) {
	sd := &fakeSystemd{status: status{Known: true, Failed: true}}
	m := testMonitor(t, sd, &fakeSender{})

	m.CheckOnce(context.Background())

	if sd.restarts != 1 {
		t.Fatalf("neustarts = %d, erwartet 1", sd.restarts)
	}
	if got := m.state.restarts(unit); got != 1 {
		t.Fatalf("zähler = %d, erwartet 1", got)
	}
}

func TestNachDerGrenzeKeinNeustartMehrSondernMeldung(t *testing.T) {
	sd := &fakeSystemd{status: status{Known: true, Failed: true}}
	sender := &fakeSender{}
	m := testMonitor(t, sd, sender)

	// Vier Durchläufe bei einer Grenze von drei: drei Neustarts, danach
	// die Meldung.
	for range 4 {
		m.CheckOnce(context.Background())
	}

	if sd.restarts != 3 {
		t.Fatalf("neustarts = %d, erwartet 3 (die eingestellte grenze)", sd.restarts)
	}
	if len(sender.subjects) != 1 {
		t.Fatalf("meldungen = %d, erwartet genau 1", len(sender.subjects))
	}
	if !strings.Contains(sender.subjects[0], "3-mal") || !strings.Contains(sender.subjects[0], "vmh03") {
		t.Errorf("betreff nennt weder anzahl noch node: %q", sender.subjects[0])
	}
	if !strings.Contains(sender.bodies[0], unit) {
		t.Errorf("text nennt die unit nicht: %q", sender.bodies[0])
	}
}

func TestMeldungWiederholtSichNicht(t *testing.T) {
	sd := &fakeSystemd{status: status{Known: true, Failed: true}}
	sender := &fakeSender{}
	m := testMonitor(t, sd, sender)

	for range 20 {
		m.CheckOnce(context.Background())
	}

	if len(sender.subjects) != 1 {
		t.Fatalf("meldungen = %d — ein dauerhaft toter dienst darf nicht alle 30s mailen", len(sender.subjects))
	}
}

func TestGescheiterterNeustartVerbrauchtEinenVersuch(t *testing.T) {
	sd := &fakeSystemd{
		status: status{Known: true, Failed: true},
		fail:   errors.New("unit nicht startbar"),
	}
	sender := &fakeSender{}
	m := testMonitor(t, sd, sender)

	for range 4 {
		m.CheckOnce(context.Background())
	}

	// Ohne diese Zählung liefe der Monitor endlos im Kreis, ohne je zu
	// melden.
	if len(sender.subjects) != 1 {
		t.Fatalf("meldungen = %d, erwartet 1", len(sender.subjects))
	}
}

func TestMisslungeneMeldungWirdWiederholt(t *testing.T) {
	sd := &fakeSystemd{status: status{Known: true, Failed: true}}
	sender := &fakeSender{fail: errors.New("mailserver nicht erreichbar")}
	m := testMonitor(t, sd, sender)

	for range 5 {
		m.CheckOnce(context.Background())
	}
	if m.state.notified(unit) {
		t.Fatal("meldung gilt als zugestellt, obwohl der versand fehlschlug")
	}

	sender.fail = nil
	m.CheckOnce(context.Background())
	if len(sender.subjects) != 1 {
		t.Fatalf("meldungen = %d, erwartet 1 nach dem geglückten versuch", len(sender.subjects))
	}
}

func TestZaehlerFaelltNachStabilerLaufzeit(t *testing.T) {
	sd := &fakeSystemd{status: status{Known: true, Failed: true}}
	m := testMonitor(t, sd, &fakeSender{})

	m.CheckOnce(context.Background())
	if got := m.state.restarts(unit); got != 1 {
		t.Fatalf("zähler = %d, erwartet 1", got)
	}

	sd.status = status{Known: true, Active: true, Since: jetzt.Add(-25 * time.Hour)}
	m.CheckOnce(context.Background())

	if got := m.state.restarts(unit); got != 0 {
		t.Fatalf("zähler = %d, erwartet 0 nach 25h laufzeit", got)
	}
}

func TestZaehlerBleibtSolangeDieLaufzeitZuKurzIst(t *testing.T) {
	sd := &fakeSystemd{status: status{Known: true, Failed: true}}
	m := testMonitor(t, sd, &fakeSender{})

	m.CheckOnce(context.Background())
	sd.status = status{Known: true, Active: true, Since: jetzt.Add(-23 * time.Hour)}
	m.CheckOnce(context.Background())

	if got := m.state.restarts(unit); got != 1 {
		t.Fatalf("zähler = %d, erwartet 1 — 23h sind noch keine 24h", got)
	}
}

func TestAngehaltenerDienstBleibtAngehalten(t *testing.T) {
	// Weder aktiv noch fehlgeschlagen: Das hat jemand so gewollt.
	sd := &fakeSystemd{status: status{Known: true}}
	m := testMonitor(t, sd, &fakeSender{})

	m.CheckOnce(context.Background())

	if sd.restarts != 0 {
		t.Fatalf("neustarts = %d, ein von hand angehaltener dienst darf nicht hochgeholt werden", sd.restarts)
	}
}

func TestReportMeldetNurUndFasstNichtsAn(t *testing.T) {
	sd := &fakeSystemd{status: status{Known: true, Failed: true}}
	sender := &fakeSender{}
	m := testMonitor(t, sd, sender)
	m.settings.Mode = mode.Report

	for range 5 {
		m.CheckOnce(context.Background())
	}

	if sd.restarts != 0 {
		t.Errorf("neustarts = %d, im probelauf darf nichts angefasst werden", sd.restarts)
	}
	if len(sender.subjects) != 0 {
		t.Errorf("meldungen = %d, im probelauf geht nichts hinaus", len(sender.subjects))
	}
}

func TestUnbekannteUnitWirdNichtAngefasst(t *testing.T) {
	sd := &fakeSystemd{status: status{Known: false}}
	m := testMonitor(t, sd, &fakeSender{})

	m.CheckOnce(context.Background())

	if sd.restarts != 0 {
		t.Fatalf("neustarts = %d bei einer unit, die systemd nicht kennt", sd.restarts)
	}
}

func TestZaehlerUeberdauertEinenNeustartDesDienstes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "services.json")

	first, err := loadState(path)
	if err != nil {
		t.Fatalf("zählerstand: %v", err)
	}
	first.count(unit)
	first.count(unit)
	if err := first.save(); err != nil {
		t.Fatalf("sichern: %v", err)
	}

	second, err := loadState(path)
	if err != nil {
		t.Fatalf("erneut lesen: %v", err)
	}
	if got := second.restarts(unit); got != 2 {
		t.Fatalf("zähler = %d, erwartet 2 — sonst hebelt ein update die grenze aus", got)
	}
}

func TestZeitspanneKurz(t *testing.T) {
	tests := map[time.Duration]string{
		24 * time.Hour:                "24h",
		90 * time.Minute:              "1h30m",
		30 * time.Second:              "30s",
		90 * time.Second:              "1m30s",
		25*time.Hour + 30*time.Minute: "25h30m",
	}
	for given, want := range tests {
		if got := kurz(given); got != want {
			t.Errorf("kurz(%s) = %q, erwartet %q", given, got, want)
		}
	}
}
