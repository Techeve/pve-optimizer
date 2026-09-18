// Package advice sammelt Prüfungen, die auf ungünstige Einstellungen
// hinweisen, sie aber ausdrücklich nicht ändern.
//
// Das ist der Unterschied zum Paket rules: Eine Regel trägt an einem Gast
// nach, was fehlt. Eine Empfehlung fasst nichts an — sie sagt, was dem
// Betreiber auffallen sollte, warum es zählt und was er dagegen tut.
// Manches gehört in Menschenhand: Welche VM in welchen Sicherungsauftrag
// gehört, weiß der Dienst nicht, und er soll es auch nicht raten.
//
// Diese Grenze steckt in der Schnittstelle, nicht in einem Kommentar: Eine
// Prüfung bekommt mit Cluster einen Zugang, der keine einzige schreibende
// Methode hat.
package advice

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"pve-optimizer/internal/mode"
	"pve-optimizer/internal/proxmox"
)

// Cluster ist der lesende Blick auf Proxmox, den eine Prüfung bekommt.
type Cluster interface {
	ListGuests(ctx context.Context) ([]proxmox.Guest, error)
	NotBackedUp(ctx context.Context) ([]proxmox.Guest, error)
	Options(ctx context.Context) (proxmox.Options, error)
	ReplicationJobs(ctx context.Context) ([]proxmox.ReplicationJob, error)
	Nodes(ctx context.Context) ([]proxmox.Node, error)
	ZFSPools(ctx context.Context, node string) ([]proxmox.ZFSPool, error)
	ZFSPoolStatus(ctx context.Context, node, pool string) (proxmox.ZFSStatus, error)
}

// Finding ist ein einzelner Befund. Drei Felder, und alle drei sind Pflicht:
// Ein Hinweis ohne Begründung wird weggeklickt, einer ohne Handlungsschritt
// bleibt liegen.
type Finding struct {
	// Check ist die Prüfung, aus der der Befund stammt.
	Check string
	// Subject nennt, worum es geht — etwa "VM 3000 (LCM-Test-PVE)".
	Subject string
	// Why sagt, warum das ungünstig ist.
	Why string
	// Action sagt, was der Betreiber tut, möglichst mit dem Weg dorthin.
	Action string
}

// Findings ist eine Gruppe von Befunden mit derselben Begründung und
// demselben Handlungsschritt.
type Findings struct {
	Check    string
	Why      string
	Action   string
	Subjects []string
}

// Group fasst zusammen, was zusammengehört. Vier ungesicherte Gäste haben
// denselben Grund und denselben Handlungsschritt — die Erklärung viermal
// zu wiederholen macht die Ausgabe länger und schlechter lesbar.
//
// Zusammengefasst wird über Grund und Handlungsschritt, nicht nur über die
// Prüfung: backup_coverage meldet zweierlei, ungesicherte Gäste und
// verwaiste Einträge auf der Ignore-Liste.
func Group(findings []Finding) []Findings {
	index := map[string]int{}
	var groups []Findings

	for _, finding := range findings {
		key := finding.Check + "\x00" + finding.Why + "\x00" + finding.Action
		if at, seen := index[key]; seen {
			groups[at].Subjects = append(groups[at].Subjects, finding.Subject)
			continue
		}
		index[key] = len(groups)
		groups = append(groups, Findings{
			Check: finding.Check, Why: finding.Why, Action: finding.Action,
			Subjects: []string{finding.Subject},
		})
	}

	// Feste Reihenfolge — eine Ausgabe, die jedes Mal anders sortiert ist,
	// lässt sich nicht mit der letzten vergleichen.
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Check < groups[j].Check })
	return groups
}

// Check ist eine einzelne Prüfung.
type Check interface {
	// Name ist der Schlüssel, unter dem die Prüfung konfiguriert wird.
	Name() string
	// Mode meldet, ob die Prüfung läuft.
	Mode() mode.Mode
	// Validate weist unsinnige Optionen ab, bevor der Dienst startet.
	Validate() error
	// Run sieht nach und liefert, was aufgefallen ist.
	Run(ctx context.Context, cluster Cluster) ([]Finding, error)

	setMode(mode.Mode)
}

// Base trägt, was jede Prüfung hat.
type Base struct {
	CheckMode mode.Mode `yaml:"mode"`

	name string
}

func (b *Base) Name() string        { return b.name }
func (b *Base) Mode() mode.Mode     { return b.CheckMode }
func (b *Base) setMode(m mode.Mode) { b.CheckMode = m }

// Validate weist "enforce" ab. Eine Empfehlung schreibt nie — wer das
// einstellt, erwartet etwas, das der Dienst bewusst nicht tut, und würde
// sich still darauf verlassen.
func (b *Base) Validate() error {
	if b.CheckMode == mode.Enforce {
		return fmt.Errorf("empfehlungen ändern nichts, erlaubt sind %s und %s", mode.Off, mode.Report)
	}
	return nil
}

// catalog sind alle Prüfungen in der Reihenfolge, in der sie laufen.
// Anders als bei den Regeln sind sie von Haus aus an: Sie ändern nichts,
// und was man nicht sieht, kann man nicht abstellen.
var catalog = []func() Check{
	func() Check { return &backupCoverage{Base: Base{name: "backup_coverage", CheckMode: mode.Report}} },
	func() Check { return &bandwidthLimits{Base: Base{name: "bandwidth_limits", CheckMode: mode.Report}} },
	func() Check { return &replicationRate{Base: Base{name: "replication_rate", CheckMode: mode.Report}} },
	func() Check { return &zfsHealth{Base: Base{name: "zfs_health", CheckMode: mode.Report}} },
	func() Check { return &zfsRedundancy{Base: Base{name: "zfs_redundancy", CheckMode: mode.Report}} },
	func() Check {
		return &memoryOvercommit{Base: Base{name: "memory_overcommit", CheckMode: mode.Report}, MaxPercent: 85}
	},
}

// Names sind alle Prüfnamen in der Reihenfolge des Katalogs.
func Names() []string {
	names := make([]string, 0, len(catalog))
	for _, newCheck := range catalog {
		names = append(names, newCheck().Name())
	}
	return names
}

// Set sind die Prüfungen eines Nodes.
type Set []Check

// Build erzeugt die Prüfungen: erst die allgemeine Einstellung, dann die
// Abweichung des Nodes darüber.
func Build(general, node map[string]yaml.Node) (Set, error) {
	if err := checkNames(general, "advice"); err != nil {
		return nil, err
	}
	if err := checkNames(node, "advice des nodes"); err != nil {
		return nil, err
	}

	var set Set
	for _, newCheck := range catalog {
		check := newCheck()
		for _, source := range []map[string]yaml.Node{general, node} {
			settings, found := source[check.Name()]
			if !found {
				continue
			}
			if err := decode(&settings, check); err != nil {
				return nil, fmt.Errorf("prüfung %s: %w", check.Name(), err)
			}
		}
		if err := check.Validate(); err != nil {
			return nil, fmt.Errorf("prüfung %s: %w", check.Name(), err)
		}
		set = append(set, check)
	}
	return set, nil
}

// decode liest die Einstellungen einer Prüfung. Kurzform ist der Modus
// allein ("replication_rate: off"), Langform eine Zuordnung mit "mode" und
// den Optionen der Prüfung.
func decode(node *yaml.Node, check Check) error {
	if node.Kind == yaml.ScalarNode {
		var m mode.Mode
		if err := node.Decode(&m); err != nil {
			return err
		}
		check.setMode(m)
		return nil
	}
	if err := checkOptions(node, check); err != nil {
		return err
	}
	return node.Decode(check)
}

func checkNames(settings map[string]yaml.Node, context string) error {
	for name := range settings {
		if !known(name) {
			return fmt.Errorf("%s: unbekannte prüfung %q, es gibt %s",
				context, name, strings.Join(Names(), ", "))
		}
	}
	return nil
}

func known(name string) bool {
	for _, newCheck := range catalog {
		if newCheck().Name() == name {
			return true
		}
	}
	return false
}

// checkOptions weist Optionen ab, die die Prüfung nicht kennt — ein
// Tippfehler bliebe sonst unbemerkt.
func checkOptions(node *yaml.Node, check Check) error {
	allowed := optionNames(check)
	for i := 0; i+1 < len(node.Content); i += 2 {
		name := node.Content[i].Value
		if !allowed[name] {
			return fmt.Errorf("unbekannte option %q", name)
		}
	}
	return nil
}

func optionNames(check Check) map[string]bool {
	names := map[string]bool{}
	var collect func(reflect.Type)
	collect = func(t reflect.Type) {
		for i := range t.NumField() {
			field := t.Field(i)
			if field.Anonymous {
				collect(field.Type)
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
			if name != "" && name != "-" {
				names[name] = true
			}
		}
	}
	collect(reflect.TypeOf(check).Elem())
	return names
}

// Run lässt alle eingeschalteten Prüfungen laufen. Eine Prüfung, die
// scheitert, hält die übrigen nicht auf — sonst verdeckt ein einzelner
// API-Aussetzer alle anderen Befunde.
func (s Set) Run(ctx context.Context, cluster Cluster) ([]Finding, []error) {
	var findings []Finding
	var problems []error

	for _, check := range s {
		if check.Mode() == mode.Off {
			continue
		}
		found, err := check.Run(ctx, cluster)
		if err != nil {
			problems = append(problems, fmt.Errorf("prüfung %s: %w", check.Name(), err))
			continue
		}
		findings = append(findings, found...)
	}
	return findings, problems
}

// Modes listet die Prüfungen mit ihrem Modus — für das Protokoll beim Start.
func (s Set) Modes() string {
	parts := make([]string, 0, len(s))
	for _, check := range s {
		parts = append(parts, check.Name()+"="+string(check.Mode()))
	}
	return strings.Join(parts, " ")
}

// ignored macht aus einer Liste von VMIDs ein Nachschlagewerk.
func ignored(vmids []int) map[int]bool {
	set := make(map[int]bool, len(vmids))
	for _, vmid := range vmids {
		set[vmid] = true
	}
	return set
}

// sortedVMIDs bringt VMIDs in eine feste Reihenfolge — ein Bericht, der
// bei jedem Lauf anders aussieht, ist schlecht zu lesen.
func sortedVMIDs(vmids []int) []int {
	out := append([]int(nil), vmids...)
	sort.Ints(out)
	return out
}
