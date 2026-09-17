// Package mode trägt den Schalter, mit dem in dieser Konfiguration alles
// Eingreifende eingestellt wird: gar nicht, nur melden, oder handeln.
// Regelwerk und Dienst-Monitor teilen ihn sich, damit in der
// Konfigurationsdatei überall dieselben drei Wörter stehen.
package mode

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

type Mode string

const (
	// Off schaltet ab.
	Off Mode = "off"
	// Report protokolliert, was geschähe, greift aber nicht ein.
	Report Mode = "report"
	// Enforce greift ein.
	Enforce Mode = "enforce"
)

// UnmarshalYAML liest den Modus aus dem rohen Knotenwert statt über die
// übliche Auflösung. Nötig wegen "off": YAML kennt das seit Version 1.1
// als Wahrheitswert, und der ließe sich nicht in einen String einlesen.
func (m *Mode) UnmarshalYAML(node *yaml.Node) error {
	switch Mode(node.Value) {
	case Off, Report, Enforce:
		*m = Mode(node.Value)
		return nil
	}
	return fmt.Errorf("unbekannter modus %q, erlaubt sind %s, %s und %s",
		node.Value, Off, Report, Enforce)
}
