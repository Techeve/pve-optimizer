package report

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"pve-optimizer/internal/advice"
	"pve-optimizer/internal/mode"
	"pve-optimizer/internal/proxmox"
)

var jetzt = time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)

// fakeCluster liefert genau einen Befund: ein ungesicherter Gast.
type fakeCluster struct {
	offen  []proxmox.Guest
	fehler error
}

func (f *fakeCluster) ListGuests(context.Context) ([]proxmox.Guest, error) {
	return f.offen, f.fehler
}
func (f *fakeCluster) NotBackedUp(context.Context) ([]proxmox.Guest, error) {
	return f.offen, f.fehler
}
func (f *fakeCluster) Options(context.Context) (proxmox.Options, error) {
	// Eine gesetzte Grenze, damit nur die Sicherung Befunde liefert.
	return proxmox.Options{BandwidthLimits: map[string]json.RawMessage{
		"default": json.RawMessage("102400"),
	}}, f.fehler
}
func (f *fakeCluster) ReplicationJobs(context.Context) ([]proxmox.ReplicationJob, error) {
	return nil, f.fehler
}
func (f *fakeCluster) Nodes(context.Context) ([]proxmox.Node, error) { return nil, f.fehler }
func (f *fakeCluster) ZFSPools(context.Context, string) ([]proxmox.ZFSPool, error) {
	return nil, f.fehler
}
func (f *fakeCluster) ZFSPoolStatus(context.Context, string, string) (proxmox.ZFSStatus, error) {
	return proxmox.ZFSStatus{}, f.fehler
}

type fakeSender struct {
	subjects []string
	bodies   []string
	fehler   error
}

func (f *fakeSender) Send(subject, body string) error {
	if f.fehler != nil {
		return f.fehler
	}
	f.subjects = append(f.subjects, subject)
	f.bodies = append(f.bodies, body)
	return nil
}
func (f *fakeSender) Describe() string { return "test" }

func checks(t *testing.T) advice.Set {
	t.Helper()
	var parsed map[string]yaml.Node
	// Nur die Sicherungsprüfung soll Befunde liefern.
	aus := "replication_rate: off\nzfs_health: off\nzfs_redundancy: off\nmemory_overcommit: off\n"
	if err := yaml.Unmarshal([]byte(aus), &parsed); err != nil {
		t.Fatalf("testkonfiguration: %v", err)
	}
	set, err := advice.Build(parsed, nil)
	if err != nil {
		t.Fatalf("advice.Build() = %v", err)
	}
	return set
}

func testReporter(t *testing.T, cluster advice.Cluster, sender *fakeSender) *Reporter {
	t.Helper()

	settings := DefaultSettings()
	settings.Mode = mode.Enforce
	settings.Node = "vmh01"

	loaded, err := loadState(filepath.Join(t.TempDir(), "report.json"))
	if err != nil {
		t.Fatalf("stand: %v", err)
	}
	return &Reporter{
		settings: settings, node: "vmh01", checks: checks(t), cluster: cluster,
		mail: sender, state: loaded,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		now: func() time.Time { return jetzt },
	}
}

func ungesichert() *fakeCluster {
	return &fakeCluster{offen: []proxmox.Guest{
		{VMID: 3000, Node: "vmh02", Name: "Test-VM", Kind: proxmox.KindQemu},
	}}
}

func TestErsterBerichtGehtSofortHinaus(t *testing.T) {
	// Sonst zeigt sich erst in einer Woche, ob der Mailweg überhaupt steht.
	sender := &fakeSender{}
	r := testReporter(t, ungesichert(), sender)

	r.CheckOnce(context.Background())

	if len(sender.subjects) != 1 {
		t.Fatalf("Mails = %d, erwartet 1", len(sender.subjects))
	}
	if !strings.Contains(sender.subjects[0], "vmh01") {
		t.Errorf("Betreff = %q, erwartet den Node", sender.subjects[0])
	}
	if !strings.Contains(sender.bodies[0], "Test-VM") {
		t.Errorf("Text nennt den Gast nicht: %q", sender.bodies[0])
	}
}

func TestVorAblaufDesAbstandsPassiertNichts(t *testing.T) {
	sender := &fakeSender{}
	r := testReporter(t, ungesichert(), sender)

	r.CheckOnce(context.Background())
	for range 5 {
		r.CheckOnce(context.Background())
	}

	if len(sender.subjects) != 1 {
		t.Fatalf("Mails = %d, erwartet genau 1 innerhalb eines Abstands", len(sender.subjects))
	}
}

func TestNachAblaufDesAbstandsWiederBericht(t *testing.T) {
	sender := &fakeSender{}
	r := testReporter(t, ungesichert(), sender)

	r.CheckOnce(context.Background())
	r.now = func() time.Time { return jetzt.Add(8 * 24 * time.Hour) }
	r.CheckOnce(context.Background())

	if len(sender.subjects) != 2 {
		t.Fatalf("Mails = %d, erwartet 2", len(sender.subjects))
	}
}

func TestOhneBefundeKeineMail(t *testing.T) {
	// Eine Mail, die jede Woche "alles in Ordnung" sagt, liest nach dem
	// vierten Mal niemand mehr.
	sender := &fakeSender{}
	r := testReporter(t, &fakeCluster{}, sender)

	r.CheckOnce(context.Background())

	if len(sender.subjects) != 0 {
		t.Fatalf("Mails = %d, erwartet keine", len(sender.subjects))
	}
	if r.state.lastSent().IsZero() {
		t.Error("der Durchlauf gilt nicht als erledigt — der nächste käme sofort wieder")
	}
}

func TestGescheiterterVersandWirdWiederholt(t *testing.T) {
	sender := &fakeSender{fehler: errors.New("mailserver weg")}
	r := testReporter(t, ungesichert(), sender)

	r.CheckOnce(context.Background())
	if !r.state.lastSent().IsZero() {
		t.Fatal("der Bericht gilt als erledigt, obwohl der Versand fehlschlug")
	}

	sender.fehler = nil
	r.CheckOnce(context.Background())
	if len(sender.subjects) != 1 {
		t.Fatalf("Mails = %d, erwartet 1 nach dem geglückten Versuch", len(sender.subjects))
	}
}

func TestAussetzerDerAPIUeberspringtDenBerichtNicht(t *testing.T) {
	// Sonst fiele der Bericht dieser Woche aus, weil die API zwei Minuten
	// nicht da war.
	cluster := &fakeCluster{fehler: errors.New("API weg")}
	sender := &fakeSender{}
	r := testReporter(t, cluster, sender)

	r.CheckOnce(context.Background())
	if !r.state.lastSent().IsZero() {
		t.Fatal("der Bericht gilt als erledigt, obwohl keine Prüfung durchlief")
	}

	cluster.fehler = nil
	cluster.offen = ungesichert().offen
	r.CheckOnce(context.Background())
	if len(sender.subjects) != 1 {
		t.Fatalf("Mails = %d, erwartet 1 nach dem geglückten Durchlauf", len(sender.subjects))
	}
}

func TestProbelaufVerschicktNichts(t *testing.T) {
	sender := &fakeSender{}
	r := testReporter(t, ungesichert(), sender)
	r.settings.Mode = mode.Report

	r.CheckOnce(context.Background())

	if len(sender.subjects) != 0 {
		t.Errorf("Mails = %d, im Probelauf geht nichts hinaus", len(sender.subjects))
	}
	if r.state.lastSent().IsZero() {
		t.Error("der Durchlauf sollte trotzdem als erledigt gelten")
	}
}

func TestNurDerZustaendigeNodeVerschickt(t *testing.T) {
	r := testReporter(t, ungesichert(), &fakeSender{})
	if !r.Responsible() {
		t.Error("vmh01 sollte zuständig sein")
	}

	r.node = "vmh02"
	if r.Responsible() {
		t.Error("vmh02 ist nicht zuständig — sonst gingen drei gleiche Mails hinaus")
	}
}

func TestEinstellungenPruefen(t *testing.T) {
	scharf := func(anpassen func(*Settings)) Settings {
		settings := DefaultSettings()
		settings.Mode = mode.Enforce
		settings.Node = "vmh01"
		anpassen(&settings)
		return settings
	}

	tests := map[string]struct {
		settings Settings
		want     string
	}{
		"vorgabe ist aus":   {DefaultSettings(), ""},
		"scharf und gültig": {scharf(func(*Settings) {}), ""},
		"ohne node":         {scharf(func(s *Settings) { s.Node = "" }), "node fehlt"},
		"abstand zu kurz":   {scharf(func(s *Settings) { s.Every = time.Minute }), "every"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := test.settings.Check()
			if test.want == "" {
				if err != nil {
					t.Fatalf("unerwarteter Fehler: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Fehler = %v, erwartet ein Hinweis auf %q", err, test.want)
			}
		})
	}
}

func TestZeitpunktUeberdauertEinenNeustart(t *testing.T) {
	// Sonst käme nach jedem Paketwechsel eine Mail.
	path := filepath.Join(t.TempDir(), "report.json")

	first, err := loadState(path)
	if err != nil {
		t.Fatalf("stand: %v", err)
	}
	first.setLastSent(jetzt)
	if err := first.save(); err != nil {
		t.Fatalf("sichern: %v", err)
	}

	second, err := loadState(path)
	if err != nil {
		t.Fatalf("erneut lesen: %v", err)
	}
	if !second.lastSent().Equal(jetzt) {
		t.Fatalf("lastSent = %s, erwartet %s", second.lastSent(), jetzt)
	}
}
