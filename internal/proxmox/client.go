// Package proxmox kapselt den Zugriff auf Proxmox — wahlweise über die
// Cluster-API oder über pvesh auf dem Node selbst.
package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// Client ist der Zugriff auf Proxmox, den der Dienst braucht.
type Client interface {
	// RecentTasks liefert die jüngsten Aufgaben des Clusters.
	RecentTasks(ctx context.Context) ([]Task, error)
	// VMConfig liefert die Konfiguration einer VM als Schlüssel-Wert-Paare.
	VMConfig(ctx context.Context, node string, vmid int) (map[string]string, error)
	// UpdateVMConfig schreibt die übergebenen Felder in die VM-Konfiguration.
	UpdateVMConfig(ctx context.Context, node string, vmid int, fields map[string]string) error
}

// Task ist eine Proxmox-Aufgabe, so weit der Dienst sie auswertet.
type Task struct {
	UPID    string `json:"upid"`
	Node    string `json:"node"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Status  string `json:"status"`
	EndTime int64  `json:"endtime"`
}

// Finished meldet erfolgreich abgeschlossene Aufgaben. Proxmox schreibt für
// den Erfolgsfall genau "OK"; alles andere ist ein Fehlertext.
func (t Task) Finished() bool {
	return t.EndTime > 0 && t.Status == "OK"
}

// VMID liefert die Nummer der VM, auf die sich die Aufgabe bezieht. Bei
// Aufgaben ohne VM-Bezug ist der zweite Rückgabewert false.
func (t Task) VMID() (int, bool) {
	id, err := strconv.Atoi(t.ID)
	if err != nil {
		return 0, false
	}
	return id, true
}

// decodeConfig wandelt die Konfigurationsantwort in Zeichenketten. Proxmox
// liefert Zahlen als JSON-Zahl und Platten als Zeichenkette — der Dienst
// braucht beides einheitlich als Text.
func decodeConfig(raw map[string]json.RawMessage) map[string]string {
	config := make(map[string]string, len(raw))
	for key, value := range raw {
		var asString string
		if err := json.Unmarshal(value, &asString); err == nil {
			config[key] = asString
			continue
		}
		var asNumber json.Number
		if err := json.Unmarshal(value, &asNumber); err == nil {
			config[key] = asNumber.String()
			continue
		}
		config[key] = string(value)
	}
	return config
}

// vmPath ist der API-Pfad zur Konfiguration einer VM.
func vmPath(node string, vmid int) string {
	return fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid)
}
