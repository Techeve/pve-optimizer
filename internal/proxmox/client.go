// Package proxmox kapselt den Zugriff auf Proxmox — wahlweise über die
// Cluster-API oder über pvesh auf dem Node selbst.
package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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
	// Storages liefert die eingerichteten Speicher.
	Storages(ctx context.Context) ([]Storage, error)
	// Nodes liefert die Nodes des Clusters.
	Nodes(ctx context.Context) ([]Node, error)
	// ZFSPools liefert die ZFS-Pools eines Nodes.
	ZFSPools(ctx context.Context, node string) ([]ZFSPool, error)
	// ZFSPoolStatus liefert den ausführlichen Zustand eines Pools.
	ZFSPoolStatus(ctx context.Context, node, pool string) (ZFSStatus, error)
}

// Node ist ein Node des Clusters.
type Node struct {
	Name   string `json:"node"`
	Status string `json:"status"`
	// MaxMem ist der Hauptspeicher des Nodes in Bytes. Bei einem Node,
	// der offline ist, fehlt er.
	MaxMem int64 `json:"maxmem"`
	Mem    int64 `json:"mem"`
}

// Online meldet, ob der Node erreichbar ist. Ein offline stehender Node
// liefert keine Werte — Prüfungen müssen ihn auslassen statt ihn als
// "0 Byte Speicher" zu behandeln.
func (n Node) Online() bool { return n.Status == "online" }

// ZFSPool ist ein ZFS-Pool aus der Übersicht.
type ZFSPool struct {
	Name   string `json:"name"`
	Health string `json:"health"`
	Size   int64  `json:"size"`
	Alloc  int64  `json:"alloc"`
	Free   int64  `json:"free"`
}

// ZFSStatus ist der ausführliche Zustand eines Pools.
type ZFSStatus struct {
	Name  string `json:"name"`
	State string `json:"state"`
	// Errors ist im guten Fall "No known data errors".
	Errors string `json:"errors"`
	// Status trägt den Hinweistext von ZFS, sofern es einen gibt.
	Status string `json:"status"`
	Scan   string `json:"scan"`
	// Children sind die virtuellen Geräte, verschachtelt wie in
	// "zpool status".
	Children []ZFSVdev `json:"children"`
}

// ZFSVdev ist ein virtuelles Gerät im Baum eines Pools.
type ZFSVdev struct {
	Name     string    `json:"name"`
	State    string    `json:"state"`
	Read     int64     `json:"read"`
	Write    int64     `json:"write"`
	Cksum    int64     `json:"cksum"`
	Leaf     int       `json:"leaf"`
	Children []ZFSVdev `json:"children"`
}

// IsLeaf meldet, ob das Gerät eine echte Platte ist und kein Verbund.
func (v ZFSVdev) IsLeaf() bool { return v.Leaf == 1 }

// Faulty meldet, ob ZFS an diesem Gerät Fehler gezählt hat.
func (v ZFSVdev) Faulty() bool { return v.Read > 0 || v.Write > 0 || v.Cksum > 0 }

// Healthy meldet, ob der Pool ohne Befund dasteht.
func (s ZFSStatus) Healthy() bool {
	return s.State == "ONLINE" && s.Errors == "No known data errors" && len(s.FaultyDevices()) == 0
}

// FaultyDevices sind die Geräte mit Fehlerzählern, in der Reihenfolge des
// Baums.
func (s ZFSStatus) FaultyDevices() []ZFSVdev {
	var faulty []ZFSVdev
	var walk func([]ZFSVdev)
	walk = func(vdevs []ZFSVdev) {
		for _, vdev := range vdevs {
			if vdev.IsLeaf() && vdev.Faulty() {
				faulty = append(faulty, vdev)
			}
			walk(vdev.Children)
		}
	}
	walk(s.Children)
	return faulty
}

// Redundant meldet, ob der Pool einen Ausfall ausgleichen kann.
//
// Gesucht wird ein Verbund im Baum — Spiegel, RAID-Z oder dRAID. Ohne
// einen solchen hat ZFS keine zweite Kopie: Es erkennt einen Fehler dann
// zwar, kann ihn aber nicht beheben.
//
// Die Prüfung schaut auf die Namen, die ZFS vergibt. Ein gespiegeltes
// Protokollgerät neben einer einzelnen Datenplatte würde hier
// fälschlich als redundant durchgehen — selten genug, um die einfache
// Regel zu rechtfertigen.
func (s ZFSStatus) Redundant() bool {
	var found bool
	var walk func([]ZFSVdev)
	walk = func(vdevs []ZFSVdev) {
		for _, vdev := range vdevs {
			name := strings.ToLower(vdev.Name)
			if !vdev.IsLeaf() && (strings.HasPrefix(name, "mirror") ||
				strings.HasPrefix(name, "raidz") || strings.HasPrefix(name, "draid")) {
				found = true
				return
			}
			walk(vdev.Children)
		}
	}
	walk(s.Children)
	return found
}

// Storage ist ein eingerichteter Speicher.
type Storage struct {
	Name string `json:"storage"`
	Type string `json:"type"`
	// Content nennt, was der Speicher aufnimmt, durch Komma getrennt —
	// etwa "images,rootdir" oder "backup,snippets".
	Content string `json:"content"`
}

// HoldsGuests meldet, ob auf dem Speicher Gastplatten liegen können. Nur
// die sind für die Drosselung interessant; ein Speicher für ISO-Abbilder
// oder Sicherungen braucht kein Profil.
func (s Storage) HoldsGuests() bool {
	for _, kind := range strings.Split(s.Content, ",") {
		switch strings.TrimSpace(kind) {
		case "images", "rootdir":
			return true
		}
	}
	return false
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
	// MaxMem ist der zugesagte Hauptspeicher in Bytes.
	MaxMem int64 `json:"maxmem"`
	// Status ist "running" oder "stopped".
	Status string `json:"status"`
	// Template kennzeichnet Vorlagen. Die laufen nie und zählen nirgends
	// mit.
	Template int `json:"template"`
}

// Running meldet, ob der Gast läuft — und keine Vorlage ist.
func (g Guest) Running() bool { return g.Status == "running" && g.Template == 0 }

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
