package watcher

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pve-optimizer/internal/config"
	"pve-optimizer/internal/limits"
	"pve-optimizer/internal/proxmox"
)

// fakeClient ersetzt Proxmox im Test und merkt sich, was geschrieben wurde.
type fakeClient struct {
	tasks   []proxmox.Task
	guests  []proxmox.Guest
	configs map[int]map[string]string
	updates map[int]map[string]string
	// kinds merkt sich, unter welcher Gastart ein Gast angefasst wurde —
	// VMs und Container liegen bei Proxmox unter verschiedenen Pfaden.
	kinds map[int]proxmox.Kind
}

func (f *fakeClient) RecentTasks(context.Context) ([]proxmox.Task, error) {
	return f.tasks, nil
}

func (f *fakeClient) ListGuests(context.Context) ([]proxmox.Guest, error) {
	return f.guests, nil
}

func (f *fakeClient) GuestConfig(_ context.Context, _ string, kind proxmox.Kind, vmid int) (map[string]string, error) {
	if f.kinds == nil {
		f.kinds = map[int]proxmox.Kind{}
	}
	f.kinds[vmid] = kind
	return f.configs[vmid], nil
}

func (f *fakeClient) UpdateGuestConfig(
	_ context.Context, _ string, _ proxmox.Kind, vmid int, fields map[string]string,
) error {
	if f.updates == nil {
		f.updates = map[int]map[string]string{}
	}
	f.updates[vmid] = fields
	return nil
}

func testConfig(t *testing.T, dryRun bool) *config.Config {
	t.Helper()
	return &config.Config{
		Mode:         config.ModeLocal,
		PollInterval: time.Second,
		DryRun:       dryRun,
		StateFile:    filepath.Join(t.TempDir(), "state.json"),
		Defaults:     limits.Profile{"mbps_rd": 200, "mbps_wr": 150},
		Pools: map[string]limits.Profile{
			"schnell": {"mbps_rd": 900, "mbps_wr": 800},
		},
	}
}

func newTestWatcher(t *testing.T, cfg *config.Config, client proxmox.Client) *Watcher {
	t.Helper()
	w, err := New(cfg, client, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	// Alles im Test ist neuer als der Startzeitpunkt.
	w.state.LastEndTime = 0
	return w
}

func TestCheckOnceErgaenztFehlendeBegrenzungen(t *testing.T) {
	client := &fakeClient{
		tasks: []proxmox.Task{
			{UPID: "UPID:a", Node: "vmh02", Type: "qmrestore", ID: "100", Status: "OK", EndTime: 1000},
		},
		configs: map[int]map[string]string{
			100: {
				"scsi0": "local-pool:vm-100-disk-0,size=32G",
				"cores": "4",
			},
		},
	}

	w := newTestWatcher(t, testConfig(t, false), client)
	if err := w.checkOnce(context.Background()); err != nil {
		t.Fatalf("checkOnce() = %v", err)
	}

	written, ok := client.updates[100]["scsi0"]
	if !ok {
		t.Fatal("scsi0 wurde nicht geschrieben")
	}
	for _, want := range []string{"mbps_rd=200", "mbps_wr=150", "size=32G"} {
		if !strings.Contains(written, want) {
			t.Errorf("%q fehlt in %q", want, written)
		}
	}
}

func TestCheckOnceNutztPoolProfil(t *testing.T) {
	client := &fakeClient{
		tasks: []proxmox.Task{
			{UPID: "UPID:b", Node: "vmh03", Type: "qmcreate", ID: "101", Status: "OK", EndTime: 1000},
		},
		configs: map[int]map[string]string{
			101: {"virtio0": "schnell:vm-101-disk-0,size=8G"},
		},
	}

	w := newTestWatcher(t, testConfig(t, false), client)
	if err := w.checkOnce(context.Background()); err != nil {
		t.Fatalf("checkOnce() = %v", err)
	}

	written := client.updates[101]["virtio0"]
	if !strings.Contains(written, "mbps_rd=900") {
		t.Errorf("pool-profil wurde nicht verwendet: %q", written)
	}
}

func TestCheckOnceLaesstBestehendeUndCDROMInRuhe(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]string
	}{
		{"platte bereits vollstaendig begrenzt", map[string]string{
			"scsi0": "local-pool:vm-102-disk-0,size=32G,mbps_rd=10,mbps_wr=10",
		}},
		{"cdrom wird uebersprungen", map[string]string{
			"ide2": "local:iso/debian.iso,media=cdrom",
		}},
		{"efidisk wird uebersprungen", map[string]string{
			"efidisk0": "local-pool:vm-102-disk-1,size=4M",
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{
				tasks: []proxmox.Task{
					{UPID: "UPID:c", Node: "vmh02", Type: "qmrestore", ID: "102", Status: "OK", EndTime: 1000},
				},
				configs: map[int]map[string]string{102: tc.config},
			}

			w := newTestWatcher(t, testConfig(t, false), client)
			if err := w.checkOnce(context.Background()); err != nil {
				t.Fatalf("checkOnce() = %v", err)
			}
			if len(client.updates) != 0 {
				t.Errorf("es haette nichts geschrieben werden duerfen, bekommen: %v", client.updates)
			}
		})
	}
}

func TestCheckOnceIgnoriertUnpassendeAufgaben(t *testing.T) {
	tests := []struct {
		name string
		task proxmox.Task
	}{
		{"fehlgeschlagen", proxmox.Task{Node: "vmh02", Type: "qmrestore", ID: "103", Status: "fehler", EndTime: 1000}},
		{"noch nicht fertig", proxmox.Task{Node: "vmh02", Type: "qmrestore", ID: "103", Status: "OK", EndTime: 0}},
		{"anderer typ", proxmox.Task{Node: "vmh02", Type: "vzdump", ID: "103", Status: "OK", EndTime: 1000}},
		{"container", proxmox.Task{Node: "vmh02", Type: "vzrestore", ID: "103", Status: "OK", EndTime: 1000}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{
				tasks:   []proxmox.Task{tc.task},
				configs: map[int]map[string]string{103: {"scsi0": "local-pool:vm-103-disk-0,size=32G"}},
			}

			w := newTestWatcher(t, testConfig(t, false), client)
			if err := w.checkOnce(context.Background()); err != nil {
				t.Fatalf("checkOnce() = %v", err)
			}
			if len(client.updates) != 0 {
				t.Errorf("aufgabe haette ignoriert werden muessen, bekommen: %v", client.updates)
			}
		})
	}
}

func TestCheckOnceDryRunSchreibtNicht(t *testing.T) {
	client := &fakeClient{
		tasks: []proxmox.Task{
			{UPID: "UPID:d", Node: "vmh02", Type: "qmcreate", ID: "104", Status: "OK", EndTime: 1000},
		},
		configs: map[int]map[string]string{
			104: {"scsi0": "local-pool:vm-104-disk-0,size=32G"},
		},
	}

	w := newTestWatcher(t, testConfig(t, true), client)
	if err := w.checkOnce(context.Background()); err != nil {
		t.Fatalf("checkOnce() = %v", err)
	}
	if len(client.updates) != 0 {
		t.Errorf("dry_run darf nichts schreiben, bekommen: %v", client.updates)
	}
}

func TestCheckOnceVerarbeitetAufgabeNurEinmal(t *testing.T) {
	client := &fakeClient{
		tasks: []proxmox.Task{
			{UPID: "UPID:e", Node: "vmh02", Type: "qmrestore", ID: "105", Status: "OK", EndTime: 1000},
		},
		configs: map[int]map[string]string{
			105: {"scsi0": "local-pool:vm-105-disk-0,size=32G"},
		},
	}

	cfg := testConfig(t, false)
	w := newTestWatcher(t, cfg, client)
	if err := w.checkOnce(context.Background()); err != nil {
		t.Fatalf("erster checkOnce() = %v", err)
	}
	if len(client.updates) != 1 {
		t.Fatalf("erster Durchlauf haette schreiben muessen, bekommen: %v", client.updates)
	}

	client.updates = nil
	if err := w.checkOnce(context.Background()); err != nil {
		t.Fatalf("zweiter checkOnce() = %v", err)
	}
	if len(client.updates) != 0 {
		t.Errorf("aufgabe wurde erneut verarbeitet: %v", client.updates)
	}

	state, err := LoadState(cfg.StateFile)
	if err != nil {
		t.Fatalf("LoadState() = %v", err)
	}
	if state.LastEndTime != 1000 {
		t.Errorf("LastEndTime = %d, erwartet 1000", state.LastEndTime)
	}
}

func TestSweepGehtAlleVorhandenenVMsDurch(t *testing.T) {
	client := &fakeClient{
		guests: []proxmox.Guest{
			{VMID: 200, Node: "vmh02", Name: "ohne-limits", Kind: proxmox.KindQemu},
			{VMID: 201, Node: "vmh03", Name: "schon-begrenzt", Kind: proxmox.KindQemu},
		},
		configs: map[int]map[string]string{
			200: {"scsi0": "local-pool:vm-200-disk-0,size=32G"},
			201: {"scsi0": "local-pool:vm-201-disk-0,size=32G,mbps_rd=10,mbps_wr=10"},
		},
	}

	w := newTestWatcher(t, testConfig(t, false), client)
	if err := w.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() = %v", err)
	}

	if _, found := client.updates[200]; !found {
		t.Error("vm 200 haette angepasst werden muessen")
	}
	if _, found := client.updates[201]; found {
		t.Error("vm 201 war bereits begrenzt und darf nicht angefasst werden")
	}
}

func TestSweepDryRunSchreibtNicht(t *testing.T) {
	client := &fakeClient{
		guests:  []proxmox.Guest{{VMID: 202, Node: "vmh02", Kind: proxmox.KindQemu}},
		configs: map[int]map[string]string{202: {"scsi0": "local-pool:vm-202-disk-0,size=32G"}},
	}

	w := newTestWatcher(t, testConfig(t, true), client)
	if err := w.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() = %v", err)
	}
	if len(client.updates) != 0 {
		t.Errorf("dry_run darf nichts schreiben, bekommen: %v", client.updates)
	}
}

// Laeuft der Dienst auf jedem Node, darf jede Instanz nur ihre eigenen VMs
// anfassen — sonst nehmen sich mehrere dieselbe VM gleichzeitig vor.
func TestNurEigenerNode(t *testing.T) {
	cfg := testConfig(t, false)
	restrict := true
	cfg.OnlyOwnNode = &restrict
	cfg.Node = "vmh03"

	client := &fakeClient{
		tasks: []proxmox.Task{
			{UPID: "UPID:f", Node: "vmh02", Type: "qmrestore", ID: "300", Status: "OK", EndTime: 1000},
			{UPID: "UPID:g", Node: "vmh03", Type: "qmrestore", ID: "301", Status: "OK", EndTime: 1000},
		},
		guests: []proxmox.Guest{
			{VMID: 300, Node: "vmh02", Kind: proxmox.KindQemu},
			{VMID: 301, Node: "vmh03", Kind: proxmox.KindQemu},
		},
		configs: map[int]map[string]string{
			300: {"scsi0": "local-pool:vm-300-disk-0,size=32G"},
			301: {"scsi0": "local-pool:vm-301-disk-0,size=32G"},
		},
	}

	w := newTestWatcher(t, cfg, client)
	if err := w.checkOnce(context.Background()); err != nil {
		t.Fatalf("checkOnce() = %v", err)
	}
	if _, found := client.updates[300]; found {
		t.Error("vm auf einem fremden node darf nicht angefasst werden")
	}
	if _, found := client.updates[301]; !found {
		t.Error("vm auf dem eigenen node haette angepasst werden muessen")
	}

	client.updates = nil
	if err := w.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() = %v", err)
	}
	if _, found := client.updates[300]; found {
		t.Error("Sweep darf fremde nodes nicht anfassen")
	}
	if _, found := client.updates[301]; !found {
		t.Error("Sweep haette den eigenen node anpassen muessen")
	}
}

// failingClient laesst das Schreiben scheitern — wie ein kurzzeitig
// schreibgeschuetztes /etc/pve oder ein Aussetzer der API.
type failingClient struct {
	fakeClient
	failFor map[int]bool
}

func (f *failingClient) UpdateGuestConfig(
	ctx context.Context, node string, kind proxmox.Kind, vmid int, fields map[string]string,
) error {
	if f.failFor[vmid] {
		return errors.New("read-only file system")
	}
	return f.fakeClient.UpdateGuestConfig(ctx, node, kind, vmid, fields)
}

// Eine fehlgeschlagene Aufgabe darf nicht als erledigt gelten: Sonst bliebe
// die VM dauerhaft ungedrosselt, obwohl der Fehler nur voruebergehend war.
func TestFehlgeschlageneAufgabeWirdErneutVersucht(t *testing.T) {
	client := &failingClient{
		fakeClient: fakeClient{
			tasks: []proxmox.Task{
				{UPID: "UPID:x", Node: "vmh03", Type: "qmcreate", ID: "400", Status: "OK", EndTime: 1000},
			},
			configs: map[int]map[string]string{
				400: {"scsi0": "local-pool:vm-400-disk-0,size=32G"},
			},
		},
		failFor: map[int]bool{400: true},
	}

	cfg := testConfig(t, false)
	w := newTestWatcher(t, cfg, client)

	if err := w.checkOnce(context.Background()); err != nil {
		t.Fatalf("checkOnce() = %v", err)
	}
	if w.state.LastEndTime >= 1000 {
		t.Fatalf("LastEndTime = %d — die fehlgeschlagene Aufgabe gilt faelschlich als erledigt", w.state.LastEndTime)
	}

	// Beim naechsten Durchlauf klappt das Schreiben.
	client.failFor = nil
	if err := w.checkOnce(context.Background()); err != nil {
		t.Fatalf("zweiter checkOnce() = %v", err)
	}
	if _, found := client.updates[400]; !found {
		t.Error("die aufgabe haette erneut versucht werden muessen")
	}
	if w.state.LastEndTime != 1000 {
		t.Errorf("LastEndTime = %d, erwartet 1000 nach erfolgreichem Durchlauf", w.state.LastEndTime)
	}
}

// Eine spaetere erfolgreiche Aufgabe darf eine frueher fehlgeschlagene
// nicht ueberholen.
func TestErfolgUeberholtFehlschlagNicht(t *testing.T) {
	client := &failingClient{
		fakeClient: fakeClient{
			tasks: []proxmox.Task{
				{UPID: "UPID:y", Node: "vmh03", Type: "qmcreate", ID: "401", Status: "OK", EndTime: 1000},
				{UPID: "UPID:z", Node: "vmh03", Type: "qmcreate", ID: "402", Status: "OK", EndTime: 2000},
			},
			configs: map[int]map[string]string{
				401: {"scsi0": "local-pool:vm-401-disk-0,size=32G"},
				402: {"scsi0": "local-pool:vm-402-disk-0,size=32G"},
			},
		},
		failFor: map[int]bool{401: true},
	}

	w := newTestWatcher(t, testConfig(t, false), client)
	if err := w.checkOnce(context.Background()); err != nil {
		t.Fatalf("checkOnce() = %v", err)
	}
	if w.state.LastEndTime >= 1000 {
		t.Errorf("LastEndTime = %d — vm 402 hat die fehlgeschlagene vm 401 ueberholt", w.state.LastEndTime)
	}
}

// Der Kern der Regeln je Node: Was clusterweit scharf ist, darf ein
// einzelner Node abschalten — etwa weil dort noch ein Speicher hängt, der
// mit Discard nicht umgehen kann.
func TestRegelnJeNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
mode: local
only_own_node: false
state_file: ` + filepath.Join(t.TempDir(), "state.json") + `
defaults:
  mbps_rd: 200
rules:
  io_limits: off
  discard: enforce
nodes:
  vmh03:
    rules:
      discard: off
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("testkonfiguration schreiben: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	client := &fakeClient{
		guests: []proxmox.Guest{
			{VMID: 100, Node: "vmh02", Kind: proxmox.KindQemu},
			{VMID: 101, Node: "vmh03", Kind: proxmox.KindQemu},
		},
		configs: map[int]map[string]string{
			100: {"scsi0": "local-pool:vm-100-disk-0,size=32G"},
			101: {"scsi0": "local-pool:vm-101-disk-0,size=32G"},
		},
	}

	w := newTestWatcher(t, cfg, client)
	if err := w.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep() = %v", err)
	}

	if got := client.updates[100]["scsi0"]; !strings.Contains(got, "discard=on") {
		t.Errorf("vm 100 auf vmh02 = %q, erwartet discard=on", got)
	}
	if _, written := client.updates[101]; written {
		t.Errorf("vm 101 auf vmh03 = %v, dort ist die regel abgeschaltet", client.updates[101])
	}
}

// Ein neu angelegter Container muss genauso erkannt werden wie eine VM —
// Proxmox stellt dessen Aufgaben "vz" statt "qm" voran.
func TestNeuerContainerWirdGestaffelt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
mode: local
only_own_node: false
state_file: ` + filepath.Join(t.TempDir(), "state.json") + `
defaults:
  mbps_rd: 200
rules:
  io_limits: off
  guest_agent: enforce
  startup:
    mode: enforce
    vm:
      up: 45s
    lxc:
      up: 10s
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("testkonfiguration schreiben: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	client := &fakeClient{
		tasks: []proxmox.Task{
			{UPID: "a", Node: "vmh02", Type: "vzcreate", ID: "200", Status: "OK", EndTime: 100},
			{UPID: "b", Node: "vmh02", Type: "qmcreate", ID: "201", Status: "OK", EndTime: 101},
		},
		configs: map[int]map[string]string{
			200: {"onboot": "1", "rootfs": "local-zfs:subvol-200-disk-0,size=8G"},
			201: {"onboot": "1", "scsi0": "local-zfs:vm-201-disk-0,size=32G"},
		},
	}

	w := newTestWatcher(t, cfg, client)
	if err := w.checkOnce(context.Background()); err != nil {
		t.Fatalf("checkOnce() = %v", err)
	}

	if got := client.kinds[200]; got != proxmox.KindLXC {
		t.Errorf("gast 200 als %q angefasst, erwartet %q", got, proxmox.KindLXC)
	}
	if got := client.updates[200]["startup"]; got != "up=10" {
		t.Errorf("container 200 startup = %q, erwartet up=10", got)
	}
	if _, written := client.updates[200]["agent"]; written {
		t.Error("container 200 hat einen agent-eintrag bekommen — den kennt LXC nicht")
	}
	if got := client.updates[201]["startup"]; got != "up=45" {
		t.Errorf("vm 201 startup = %q, erwartet up=45", got)
	}
	if got := client.updates[201]["agent"]; got != "1" {
		t.Errorf("vm 201 agent = %q, erwartet 1", got)
	}
}
