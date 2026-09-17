package rules

import (
	"errors"
	"fmt"
	"strconv"

	"pve-optimizer/internal/proxmox"
)

// netRate begrenzt, wie viel über die Netzwerkkarten eines Gastes laufen
// darf. Ohne Begrenzung zieht ein einzelner Gast die Leitung des Nodes
// leer — ein Backup, ein Klon oder ein durchgedrehter Dienst genügt, und
// alle anderen Gäste auf derselben Brücke hängen mit.
//
// Wie die Staffelung betrifft die Regel beide Gastarten: Proxmox kennt
// "rate" bei VMs und Containern gleichermaßen. Getrennt einstellbar sind
// sie trotzdem — ein Container, der nur eine Weboberfläche ausliefert,
// braucht eine andere Leine als eine VM, die Sicherungen schreibt.
//
// Der Wert ist Megabyte je Sekunde, so wie Proxmox ihn führt. Eine
// Gigabit-Leitung sind also 125.
type netRate struct {
	Base `yaml:",inline"`

	// Rate gilt für beide Gastarten, solange darunter nichts Genaueres
	// steht.
	Rate float64 `yaml:"rate"`
	// VM und LXC weichen davon für eine Gastart ab. Eine Rate von 0 heißt
	// dort wie überall in dieser Konfiguration "nicht setzen" — die
	// Gastart bleibt dann unangetastet.
	VM  *bandwidth `yaml:"vm"`
	LXC *bandwidth `yaml:"lxc"`
}

type bandwidth struct {
	Rate float64 `yaml:"rate"`
}

// rateFor liefert die Begrenzung für eine Gastart. 0 heißt: nicht anfassen.
func (r *netRate) rateFor(kind proxmox.Kind) float64 {
	specific := r.VM
	if kind == proxmox.KindLXC {
		specific = r.LXC
	}
	if specific != nil {
		return specific.Rate
	}
	return r.Rate
}

func (r *netRate) Check() error {
	if r.Mode() == ModeOff {
		return nil
	}

	var anySet bool
	for _, kind := range []proxmox.Kind{proxmox.KindQemu, proxmox.KindLXC} {
		switch value := r.rateFor(kind); {
		case value == 0:
			// Diese Gastart ist bewusst ausgenommen.
		case value < 0:
			return fmt.Errorf("rate für %s darf nicht negativ sein", kind)
		default:
			anySet = true
		}
	}
	if !anySet {
		return errors.New("rate fehlt: ohne begrenzung für wenigstens eine gastart täte die regel nichts")
	}
	return nil
}

func (r *netRate) Apply(plan *Plan) {
	value := r.rateFor(plan.Kind)
	if value == 0 {
		plan.Skip(r, "net", "für "+string(plan.Kind)+" ist keine begrenzung eingestellt")
		return
	}

	networks := plan.Networks()
	if len(networks) == 0 {
		plan.Skip(r, "net", "gast hat keine netzwerkkarte")
		return
	}
	for _, key := range networks {
		plan.SetProperty(r, key, "rate", formatRate(value))
	}
}

// formatRate schreibt die Rate so knapp wie möglich: 125 bleibt "125",
// 12.5 bleibt "12.5". Proxmox nimmt eine Fließkommazahl, aber "125.000000"
// in einer Konfiguration, die Menschen lesen, will niemand.
func formatRate(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
