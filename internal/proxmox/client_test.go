package proxmox

import (
	"encoding/json"
	"testing"
)

func TestTaskFinished(t *testing.T) {
	tests := []struct {
		name string
		task Task
		want bool
	}{
		{"erfolgreich", Task{Status: "OK", EndTime: 1000}, true},
		{"noch am laufen", Task{Status: "OK", EndTime: 0}, false},
		{"fehlgeschlagen", Task{Status: "command failed", EndTime: 1000}, false},
		{"abgebrochen", Task{Status: "interrupted", EndTime: 1000}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.task.Finished(); got != tc.want {
				t.Errorf("Finished() = %v, erwartet %v", got, tc.want)
			}
		})
	}
}

func TestTaskVMID(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		want   int
		wantOK bool
	}{
		{"vm-nummer", "100", 100, true},
		{"leer", "", 0, false},
		{"kein vm-bezug", "vmh02", 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Task{ID: tc.id}.VMID()
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("VMID() = (%d, %v), erwartet (%d, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// Proxmox liefert Zahlen als JSON-Zahl und Platten als Zeichenkette. Der
// Dienst braucht beides einheitlich als Text.
func TestDecodeConfig(t *testing.T) {
	raw := map[string]json.RawMessage{
		"scsi0":  json.RawMessage(`"local-pool:vm-100-disk-0,size=32G"`),
		"cores":  json.RawMessage(`4`),
		"memory": json.RawMessage(`8192`),
		"onboot": json.RawMessage(`1`),
	}

	got := decodeConfig(raw)

	want := map[string]string{
		"scsi0":  "local-pool:vm-100-disk-0,size=32G",
		"cores":  "4",
		"memory": "8192",
		"onboot": "1",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("decodeConfig()[%q] = %q, erwartet %q", key, got[key], value)
		}
	}
}

func TestVMPath(t *testing.T) {
	if got := vmPath("vmh02", 100); got != "/nodes/vmh02/qemu/100/config" {
		t.Errorf("vmPath() = %q", got)
	}
}
