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
)

// Mode bestimmt, wie weit eine Regel geht.
type Mode string

const (
	// ModeOff schaltet die Regel ab.
	ModeOff Mode = "off"
	// ModeReport protokolliert, was die Regel ergänzen würde, schreibt aber
	// nichts.
	ModeReport Mode = "report"
	// ModeEnforce schreibt die fehlenden Werte in die VM-Konfiguration.
	ModeEnforce Mode = "enforce"
)

// UnmarshalYAML liest den Modus aus dem rohen Knotenwert statt über die
// übliche Auflösung. Nötig wegen "off": YAML kennt das seit Version 1.1
// als Wahrheitswert, und der ließe sich nicht in einen String einlesen.
func (m *Mode) UnmarshalYAML(node *yaml.Node) error {
	switch Mode(node.Value) {
	case ModeOff, ModeReport, ModeEnforce:
		*m = Mode(node.Value)
		return nil
	}
	return fmt.Errorf("unbekannter modus %q, erlaubt sind %s, %s und %s",
		node.Value, ModeOff, ModeReport, ModeEnforce)
}

// Rule ist eine einzelne Prüfung an einer VM.
type Rule interface {
	// Name ist der Schlüssel, unter dem die Regel konfiguriert wird.
	Name() string
	// Mode meldet, wie weit die Regel gehen darf.
	Mode() Mode
	// Check weist unsinnige Optionen ab, bevor der Dienst startet.
	Check() error
	// Apply trägt an der VM nach, was der Regel zufolge fehlt.
	Apply(plan *Plan)

	setMode(Mode)
}

// Base trägt, was jede Regel hat. Der Name kommt aus dem Katalog, der
// Modus aus der Konfiguration.
type Base struct {
	RuleMode Mode `yaml:"mode"`

	name string
}

func (b *Base) Name() string   { return b.name }
func (b *Base) Mode() Mode     { return b.RuleMode }
func (b *Base) Check() error   { return nil }
func (b *Base) setMode(m Mode) { b.RuleMode = m }

// catalog sind alle Regeln in der Reihenfolge, in der sie laufen. Sie
// greifen auf verschiedene Felder zu und sind voneinander unabhängig; die
// Reihenfolge bestimmt nur, wie das Protokoll aussieht.
//
// Die Vorgabemodi sind bewusst zurückhaltend: Nur die IO-Begrenzung ist
// von Haus aus scharf, alles Weitere schaltet frei, wer es haben will.
var catalog = []func() Rule{
	func() Rule { return &ioLimits{Base: Base{name: "io_limits", RuleMode: ModeEnforce}} },
	func() Rule { return &discard{Base: Base{name: "discard", RuleMode: ModeOff}} },
	func() Rule { return &ssd{Base: Base{name: "ssd", RuleMode: ModeOff}} },
	func() Rule { return &iothread{Base: Base{name: "iothread", RuleMode: ModeOff}} },
	func() Rule { return &guestAgent{Base: Base{name: "guest_agent", RuleMode: ModeOff}} },
	func() Rule { return &startup{Base: Base{name: "startup", RuleMode: ModeOff}} },
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

// Apply lässt alle eingeschalteten Regeln über den Entwurf laufen.
func (s Set) Apply(plan *Plan) {
	for _, rule := range s {
		if rule.Mode() == ModeOff {
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
