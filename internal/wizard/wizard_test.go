package wizard

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pve-optimizer/internal/proxmox"
)

// fakeClient ersetzt Proxmox im Test.
type fakeClient struct {
	storages []proxmox.Storage
	guests   []proxmox.Guest
	offen    []proxmox.Guest
	fehler   error
}

func (f *fakeClient) Storages(context.Context) ([]proxmox.Storage, error) {
	return f.storages, f.fehler
}
func (f *fakeClient) ListGuests(context.Context) ([]proxmox.Guest, error) {
	return f.guests, f.fehler
}
func (f *fakeClient) NotBackedUp(context.Context) ([]proxmox.Guest, error) {
	return f.offen, f.fehler
}
func (f *fakeClient) RecentTasks(context.Context) ([]proxmox.Task, error) { return nil, nil }
func (f *fakeClient) GuestConfig(context.Context, string, proxmox.Kind, int) (map[string]string, error) {
	return nil, nil
}
func (f *fakeClient) UpdateGuestConfig(context.Context, string, proxmox.Kind, int, map[string]string) error {
	return nil
}
func (f *fakeClient) Options(context.Context) (proxmox.Options, error) {
	return proxmox.Options{}, nil
}
func (f *fakeClient) ReplicationJobs(context.Context) ([]proxmox.ReplicationJob, error) {
	return nil, nil
}

func cluster() *fakeClient {
	return &fakeClient{
		storages: []proxmox.Storage{
			{Name: "local-pool", Type: "zfspool", Content: "images,rootdir"},
			{Name: "PBS", Type: "pbs", Content: "backup"},
			{Name: "NAS-iSO", Type: "nfs", Content: "iso,vztmpl"},
		},
		guests: []proxmox.Guest{{VMID: 100}, {VMID: 3000}},
		offen: []proxmox.Guest{
			{VMID: 3000, Name: "Test-VM", Kind: proxmox.KindQemu},
			{VMID: 101, Name: "Wegwerf-CT", Kind: proxmox.KindLXC},
		},
	}
}

// laufen fährt den Wizard mit den übergebenen Antworten durch.
func laufen(t *testing.T, client proxmox.Client, antworten string) (string, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	var out bytes.Buffer

	w := New(strings.NewReader(antworten), &out, client, "vmh01")
	w.now = func() time.Time { return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC) }

	if err := w.Run(context.Background(), path); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	return path, out.String()
}

// Der wichtigste Nachweis: Was der Wizard schreibt, muss der Dienst laden
// können. Eine Datei, die er nicht versteht, wäre schlechter als keine.
func TestVorgabenErgebenEineLadbareKonfiguration(t *testing.T) {
	path, ausgabe := laufen(t, cluster(), "")

	if !strings.Contains(ausgabe, "wurde geprüft und ist ladbar") {
		t.Fatalf("die Prüfung fehlt in der Ausgabe:\n%s", ausgabe)
	}
	inhalt, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("geschriebene Datei lesen: %v", err)
	}
	for _, want := range []string{"mode: local", "defaults:", "mbps_rd: 200", "rules:", "advice:"} {
		if !strings.Contains(string(inhalt), want) {
			t.Errorf("in der Datei fehlt %q", want)
		}
	}
}

func TestGefundeneUmgebungWirdGezeigt(t *testing.T) {
	_, ausgabe := laufen(t, cluster(), "")

	// Nur Speicher mit Gastplatten — PBS und ISO-Ablagen brauchen kein Profil.
	if !strings.Contains(ausgabe, "local-pool") {
		t.Error("der Speicher mit Gastplatten fehlt in der Übersicht")
	}
	if strings.Contains(ausgabe, "NAS-iSO") {
		t.Error("ein Speicher ohne Gastplatten wurde als Pool angeboten")
	}
	if !strings.Contains(ausgabe, "davon ohne Sicherung:     2") {
		t.Errorf("die Zahl der ungesicherten Gäste fehlt:\n%s", ausgabe)
	}
}

func TestAntwortenLandenInDerDatei(t *testing.T) {
	// Reihenfolge: mbps_rd, mbps_wr, io_limits, discard, ssd, iothread,
	// guest_agent, startup, startup-vm, startup-lxc, net_rate, rate,
	// monitor, limit, mailserver, empfänger, bericht, abstand, ignore,
	// schreiben
	antworten := strings.Join([]string{
		"500", "400",
		"enforce", "enforce", "report", "off", "off",
		"enforce", "45s", "10s",
		"enforce", "125",
		"j", "5",
		"mail.example.com:25", "admin@example.com",
		"j", "168h",
		"101",
		"j",
	}, "\n") + "\n"

	path, _ := laufen(t, cluster(), antworten)
	inhalt, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("lesen: %v", err)
	}
	text := string(inhalt)

	for _, want := range []string{
		"mbps_rd: 500",
		"discard: enforce",
		"iothread: off",
		"up: 45s",
		"rate: 125",
		"restart_limit: 5",
		"to: admin@example.com",
		"ignore: [101]",
		"node: vmh01",
		"every: 168h",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("in der Datei fehlt %q:\n%s", want, text)
		}
	}
}

func TestVorhandeneKonfigurationWirdGesichert(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("# gewachsen\ndefaults:\n  mbps_rd: 42\n"), 0o600); err != nil {
		t.Fatalf("vorbereiten: %v", err)
	}

	var out bytes.Buffer
	w := New(strings.NewReader(""), &out, cluster(), "vmh01")
	if err := w.Run(context.Background(), path); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	gesichert, err := os.ReadFile(path + ".vor-wizard")
	if err != nil {
		t.Fatalf("die bisherige Konfiguration wurde nicht gesichert: %v", err)
	}
	if !strings.Contains(string(gesichert), "mbps_rd: 42") {
		t.Errorf("die Sicherung enthält nicht das Original: %q", gesichert)
	}
}

func TestAblehnenSchreibtNichts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	var out bytes.Buffer
	// Ausgeschriebene Antworten statt geratener Leerzeilen: Die letzte
	// Frage ist die nach dem Schreiben, und genau die wird verneint.
	antworten := strings.Join([]string{
		"500", "400",
		"enforce", "enforce", "report", "off", "off",
		"enforce", "45s", "10s",
		"off",
		"j", "3",
		"",  // kein Mailserver
		"n", // kein Bericht
		"",  // keine Ignore-Liste
		"n", // NICHT schreiben
	}, "\n") + "\n"
	w := New(strings.NewReader(antworten), &out, cluster(), "vmh01")
	if err := w.Run(context.Background(), path); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("es wurde geschrieben, obwohl abgelehnt wurde")
	}
	if !strings.Contains(out.String(), "Nichts geschrieben") {
		t.Error("die Ablehnung wurde nicht bestätigt")
	}
}

// Klemmt die API, soll der Wizard trotzdem durchlaufen — dann eben ohne
// Vorschläge.
func TestOhneClusterZugriffLaeuftErTrotzdem(t *testing.T) {
	client := &fakeClient{fehler: errors.New("pvesh nicht gefunden")}
	path, ausgabe := laufen(t, client, "")

	if !strings.Contains(ausgabe, "nicht abrufbar") {
		t.Errorf("der Hinweis auf die klemmende API fehlt:\n%s", ausgabe)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("trotzdem sollte eine Konfiguration entstehen: %v", err)
	}
}
