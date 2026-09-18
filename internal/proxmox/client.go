// Package proxmox kapselt den Zugriff auf Proxmox — wahlweise über die
// Cluster-API oder über pvesh auf dem Node selbst.
package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// Kind ist die Gastart. Proxmox führt VMs und Container getrennt: Die
// Konfiguration liegt je Art unter einem eigenen Pfad, und nicht jede
// Einstellung gibt es bei beiden.
type Kind string

const (
	// KindQemu ist eine virtuelle Maschine.
	KindQemu Kind = "qemu"
	// KindLXC ist ein Container.
	KindLXC Kind = "lxc"
)

// Client ist der Zugriff auf Proxmox, den der Dienst braucht.
type Client interface {
	// RecentTasks liefert die jüngsten Aufgaben des Clusters.
	RecentTasks(ctx context.Context) ([]Task, error)
	// ListGuests liefert alle Gäste des Clusters — VMs und Container.
	ListGuests(ctx context.Context) ([]Guest, error)
	// GuestConfig liefert die Konfiguration eines Gastes als
	// Schlüssel-Wert-Paare.
	GuestConfig(ctx context.Context, node string, kind Kind, vmid int) (map[string]string, error)
	// UpdateGuestConfig schreibt die übergebenen Felder in die
	// Konfiguration des Gastes.
	UpdateGuestConfig(ctx context.Context, node string, kind Kind, vmid int, fields map[string]string) error
	// NotBackedUp liefert die Gäste, die kein Sicherungsauftrag erfasst.
	NotBackedUp(ctx context.Context) ([]Guest, error)
	// Options liefert die Rechenzentrums-Einstellungen.
	Options(ctx context.Context) (Options, error)
	// ReplicationJobs liefert die Replikationsaufträge des Clusters.
	ReplicationJobs(ctx context.Context) ([]ReplicationJob, error)
}

// Options sind die Rechenzentrums-Einstellungen (datacenter.cfg).
type Options struct {
	// BandwidthLimits sind die Grenzen aus "bwlimit". Der Wert bleibt roh,
	// weil die Prüfung nur wissen muss, ob eine Grenze gesetzt ist —
	// Proxmox liefert je nach Fassung Zahl oder Zeichenkette.
	//
	// Achtung bei der Einheit: "bwlimit" rechnet in KiB/s, während der
	// Durchsatz an Platten und Netzwerkkarten in MB/s angegeben wird.
	BandwidthLimits map[string]json.RawMessage `json:"bwlimit"`
}

// BandwidthOperations sind die Vorgänge, für die Proxmox eine Grenze
// kennt. "default" gilt für alles, was keine eigene Grenze hat.
var BandwidthOperations = []string{"restore", "migration", "move", "clone"}

// HasBandwidthLimit meldet, ob für einen Vorgang eine Grenze greift —
// entweder seine eigene oder die allgemeine.
func (o Options) HasBandwidthLimit(operation string) bool {
	if len(o.BandwidthLimits[operation]) > 0 {
		return true
	}
	return len(o.BandwidthLimits["default"]) > 0
}

// ReplicationJob ist ein Replikationsauftrag des Clusters.
type ReplicationJob struct {
	ID     string `json:"id"`
	Guest  int    `json:"guest"`
	Source string `json:"source"`
	Target string `json:"target"`
	// Schedule bleibt leer, wenn der Auftrag die Vorgabe nutzt.
	Schedule string `json:"schedule"`
	// Rate fehlt, wenn der Auftrag ungebremst läuft.
	Rate json.RawMessage `json:"rate"`
}

// Limited meldet, ob der Auftrag eine Ratenbegrenzung trägt.
func (j ReplicationJob) Limited() bool { return len(j.Rate) > 0 }

// Guest ist ein Gast aus der Ressourcenliste des Clusters.
type Guest struct {
	VMID int    `json:"vmid"`
	Node string `json:"node"`
	Name string `json:"name"`
	Kind Kind   `json:"type"`
}

// onlyKnownKinds wirft heraus, was weder VM noch Container ist. Proxmox
// führt beide unter der Ressourcenart "vm"; was dort künftig sonst noch
// auftaucht, geht den Dienst nichts an.
func onlyKnownKinds(all []Guest) []Guest {
	var guests []Guest
	for _, guest := range all {
		if guest.Kind == KindQemu || guest.Kind == KindLXC {
			guests = append(guests, guest)
		}
	}
	return guests
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

// VMID liefert die Nummer des Gastes, auf den sich die Aufgabe bezieht.
// Bei Aufgaben ohne Gastbezug ist der zweite Rückgabewert false.
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

// guestPath ist der API-Pfad zur Konfiguration eines Gastes. Die Gastart
// ist dabei das Pfadsegment — "qemu" oder "lxc".
func guestPath(node string, kind Kind, vmid int) string {
	return fmt.Sprintf("/nodes/%s/%s/%d/config", node, kind, vmid)
}
