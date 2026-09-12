package watcher

import (
	"context"
	"io"
	"log/slog"
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
	configs map[int]map[string]string
	updates map[int]map[string]string
}

func (f *fakeClient) RecentTasks(context.Context) ([]proxmox.Task, error) {
	return f.tasks, nil
}

func (f *fakeClient) VMConfig(_ context.Context, _ string, vmid int) (map[string]string, error) {
	return f.configs[vmid], nil
}

func (f *fakeClient) UpdateVMConfig(_ context.Context, _ string, vmid int, fields map[string]string) error {
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
