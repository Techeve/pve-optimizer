// Package rules sammelt die Einstellungen, die Proxmox bei einer neuen VM
// offenlässt und die im Betrieb regelmäßig vergessen werden. Jede Regel
// prüft einen Punkt und trägt nach, was fehlt — bereits gesetzte Werte
// bleiben unangetastet.
//
// Wie weit eine Regel gehen darf, bestimmt ihr Modus: gar nicht, nur
// melden, oder schreiben. Der Modus lässt sich je Node abweichend setzen.
package rules

import (
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"

	"pve-optimizer/internal/mode"
	"pve-optimizer/internal/proxmox"
)

// Mode bestimmt, wie weit eine Regel geht. Denselben Schalter benutzt der
// Dienst-Monitor, deshalb liegt er in einem eigenen Paket.
type Mode = mode.Mode

const (
	// ModeOff schaltet die Regel ab.
	ModeOff = mode.Off
	// ModeReport protokolliert, was die Regel ergänzen würde, schreibt aber
	// nichts.
	ModeReport = mode.Report
	// ModeEnforce schreibt die fehlenden Werte in die VM-Konfiguration.
	ModeEnforce = mode.Enforce
)

// Rule ist eine einzelne Prüfung an einer VM.
type Rule interface {
	// Name ist der Schlüssel, unter dem die Regel konfiguriert wird.
	Name() string
	// Mode meldet, wie weit die Regel gehen darf.
	Mode() Mode
	// Check weist unsinnige Optionen ab, bevor der Dienst startet.
	Check() error
	// AppliesTo meldet, ob die Regel für diese Gastart überhaupt gilt.
	AppliesTo(kind proxmox.Kind) bool
	// Apply trägt am Gast nach, was der Regel zufolge fehlt.
	Apply(plan *Plan)

	setMode(Mode)
}

// Base trägt, was jede Regel hat. Name und Gastarten kommen aus dem
// Katalog, der Modus aus der Konfiguration.
type Base struct {
	RuleMode Mode `yaml:"mode"`

	name string
	// kinds sind die Gastarten, für die die Regel gilt. Leer heißt: beide.
	kinds []proxmox.Kind
}

func (b *Base) Name() string   { return b.name }
func (b *Base) Mode() Mode     { return b.RuleMode }
func (b *Base) Check() error   { return nil }
func (b *Base) setMode(m Mode) { b.RuleMode = m }

// AppliesTo meldet, ob die Regel für diese Gastart gilt. Das meiste, was
// der Dienst ergänzt, gibt es nur bei VMs — Proxmox kennt für Container
// weder Drosselung je Mountpoint noch einen Gast-Agenten.
func (b *Base) AppliesTo(kind proxmox.Kind) bool {
	if len(b.kinds) == 0 {
		return true
	}
	for _, known := range b.kinds {
		if known == kind {
			return true
		}
	}
	return false
}

// catalog sind alle Regeln in der Reihenfolge, in der sie laufen. Sie
// greifen auf verschiedene Felder zu und sind voneinander unabhängig; die
// Reihenfolge bestimmt nur, wie das Protokoll aussieht.
//
// Die Vorgabemodi sind bewusst zurückhaltend: Nur die IO-Begrenzung ist
// von Haus aus scharf, alles Weitere schaltet frei, wer es haben will.
var catalog = []func() Rule{
	func() Rule { return &ioLimits{Base: qemuOnly("io_limits", ModeEnforce)} },
	func() Rule { return &discard{Base: qemuOnly("discard", ModeOff)} },
	func() Rule { return &ssd{Base: qemuOnly("ssd", ModeOff)} },
	func() Rule { return &iothread{Base: qemuOnly("iothread", ModeOff)} },
	func() Rule { return &guestAgent{Base: qemuOnly("guest_agent", ModeOff)} },
	// Die beiden Regeln, die auch Container betreffen: Ein Node fährt
	// beide Gastarten gemeinsam hoch, und "rate" kennt Proxmox an der
	// Netzwerkkarte einer VM wie an der eines Containers.
	func() Rule { return &startup{Base: Base{name: "startup", RuleMode: ModeOff}} },
	func() Rule { return &netRate{Base: Base{name: "net_rate", RuleMode: ModeOff}} },
}

func qemuOnly(name string, mode Mode) Base {
	return Base{name: name, RuleMode: mode, kinds: []proxmox.Kind{proxmox.KindQemu}}
}

// Set sind die Regeln eines Nodes, in der Reihenfolge des Katalogs.
type Set []Rule

// Build erzeugt die Regeln für einen Node: erst die allgemeine Einstellung,
// dann die Abweichung dieses Nodes darüber. Überschrieben wird dabei nur,
// was der Node tatsächlich nennt — wer bei einer Regel nur den Modus
// abweichend setzt, behält deren übrige Optionen.
func Build(general, node map[string]yaml.Node) (Set, error) {
	if err := checkNames(general, "rules"); err != nil {
		return nil, err
	}
	if err := checkNames(node, "nodes.<node>.rules"); err != nil {
		return nil, err
	}

	set := make(Set, 0, len(catalog))
	for _, newRule := range catalog {
		rule := newRule()
		for _, source := range []map[string]yaml.Node{general, node} {
			settings, found := source[rule.Name()]
			if !found {
				continue
			}
			if err := decode(&settings, rule); err != nil {
				return nil, fmt.Errorf("regel %s: %w", rule.Name(), err)
			}
		}
		if err := rule.Check(); err != nil {
			return nil, fmt.Errorf("regel %s: %w", rule.Name(), err)
		}
		set = append(set, rule)
	}
	return set, nil
}

// decode liest die Einstellungen einer Regel. Kurzform ist der Modus allein
// ("discard: enforce"), Langform eine Zuordnung mit "mode" und den Optionen
// der Regel.
func decode(node *yaml.Node, rule Rule) error {
	if node.Kind == yaml.ScalarNode {
		var mode Mode
		if err := node.Decode(&mode); err != nil {
			return err
		}
		rule.setMode(mode)
		return nil
	}
	if err := checkOptions(node, rule); err != nil {
		return err
	}
	return node.Decode(rule)
}

// checkNames weist Regeln ab, die es nicht gibt. Ein Tippfehler bliebe
// sonst unbemerkt und die gemeinte Regel schlicht aus.
func checkNames(settings map[string]yaml.Node, context string) error {
	for name := range settings {
		if !known(name) {
			return fmt.Errorf("%s: unbekannte regel %q, es gibt %s",
				context, name, strings.Join(Names(), ", "))
		}
	}
	return nil
}

func known(name string) bool {
	for _, newRule := range catalog {
		if newRule().Name() == name {
			return true
		}
	}
	return false
}

// Names sind alle Regelnamen in der Reihenfolge des Katalogs.
func Names() []string {
	names := make([]string, 0, len(catalog))
	for _, newRule := range catalog {
		names = append(names, newRule().Name())
	}
	return names
}

// checkOptions weist Optionen ab, die die Regel nicht kennt — aus demselben
// Grund wie checkNames.
func checkOptions(node *yaml.Node, rule Rule) error {
	allowed := optionNames(rule)
	for i := 0; i+1 < len(node.Content); i += 2 {
		name := node.Content[i].Value
		if !allowed[name] {
			return fmt.Errorf("unbekannte option %q", name)
		}
	}
	return nil
}

// optionNames liest die YAML-Namen der Felder einer Regel aus deren Typ.
func optionNames(rule Rule) map[string]bool {
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
	collect(reflect.TypeOf(rule).Elem())
	return names
}

// Apply lässt alle eingeschalteten Regeln über den Entwurf laufen, soweit
// sie für dessen Gastart gelten.
func (s Set) Apply(plan *Plan) {
	for _, rule := range s {
		if rule.Mode() == ModeOff || !rule.AppliesTo(plan.Kind) {
			continue
		}
		rule.Apply(plan)
	}
}

// ReportOnly senkt jede scharfe Regel auf "report" ab — das ist der
// Probelauf, bei dem nichts geschrieben wird.
func (s Set) ReportOnly() Set {
	for _, rule := range s {
		if rule.Mode() == ModeEnforce {
			rule.setMode(ModeReport)
		}
	}
	return s
}

// Modes listet die Regeln mit ihrem Modus — für das Protokoll beim Start.
func (s Set) Modes() string {
	parts := make([]string, 0, len(s))
	for _, rule := range s {
		parts = append(parts, rule.Name()+"="+string(rule.Mode()))
	}
	return strings.Join(parts, " ")
}
