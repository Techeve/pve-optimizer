package rules

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"

	"pve-optimizer/internal/proxmox"
)

// guestAgent schaltet den QEMU-Gast-Agenten ein. Ohne ihn weiß der Host
// nicht, was im Gast vorgeht: Ein Herunterfahren bleibt ein Druck auf den
// Ausschalter, und beim Backup fehlt das Einfrieren des Dateisystems — die
// Sicherung ist dann nur so konsistent wie nach einem Stromausfall.
//
// Im Gast muss der Agent zusätzlich installiert sein. Das Kennzeichen hier
// erlaubt ihn lediglich; ist er nicht da, ändert sich nichts.
type guestAgent struct {
	Base `yaml:",inline"`

	// FstrimClonedDisks lässt Proxmox nach einem Klon oder einer
	// Verschiebung im Gast aufräumen, damit die Kopie nicht unnötig groß
	// bleibt.
	FstrimClonedDisks bool `yaml:"fstrim_cloned_disks"`
}

func (r *guestAgent) Apply(plan *Plan) {
	if current, set := plan.Value("agent"); set {
		plan.Skip(r, "agent", "bereits gesetzt: "+current)
		return
	}

	value := "1"
	if r.FstrimClonedDisks {
		value = "1,fstrim_cloned_disks=1"
	}
	plan.SetValue(r, "agent", value)
}

// startup staffelt den Start nach einem Neustart des Nodes. Ohne Abstand
// fahren alle automatisch startenden Gäste gleichzeitig hoch und erzeugen
// genau die Lastspitze, gegen die die IO-Begrenzung sonst arbeitet.
//
// Als einzige Regel betrifft sie auch Container: Ein Node fährt beide
// Gastarten gemeinsam hoch. Weil ein Container in Sekunden oben ist, eine
// VM aber erst ihr BIOS durchläuft, lässt sich der Abstand je Gastart
// getrennt einstellen.
//
// Angefasst wird nur, was ohnehin automatisch mitstartet. Ob ein Gast
// mitstarten soll, ist eine Entscheidung des Betreibers und keine
// vergessene Einstellung — "onboot" fehlt bei einem Gast, der bewusst von
// Hand gestartet wird, genauso wie bei einem, an den niemand gedacht hat.
type startup struct {
	Base `yaml:",inline"`

	// Up ist der Abstand, den Proxmox nach diesem Gast einhält, bevor der
	// nächste startet. Gilt für beide Gastarten, solange darunter nichts
	// Genaueres steht.
	Up duration `yaml:"up"`
	// VM und LXC weichen davon für eine Gastart ab. Ein Abstand von 0
	// heißt dort wie überall in dieser Konfiguration "nicht setzen" — die
	// Gastart bleibt dann unangetastet.
	VM  *delay `yaml:"vm"`
	LXC *delay `yaml:"lxc"`
}

type delay struct {
	Up duration `yaml:"up"`
}

// duration ist eine Zeitspanne wie "30s". Über die übliche Schreibweise
// hinaus nimmt sie die blanke 0 an: In dieser Konfiguration heißt 0
// überall "nicht setzen", und dafür "0s" zu verlangen wäre eine
// Stolperfalle.
type duration time.Duration

func (d *duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Value == "0" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("%q ist keine zeitspanne, erwartet etwa \"30s\"", node.Value)
	}
	*d = duration(parsed)
	return nil
}

// delayFor liefert den Abstand für eine Gastart. 0 heißt: nicht anfassen.
func (r *startup) delayFor(kind proxmox.Kind) time.Duration {
	specific := r.VM
	if kind == proxmox.KindLXC {
		specific = r.LXC
	}
	if specific != nil {
		return time.Duration(specific.Up)
	}
	return time.Duration(r.Up)
}

func (r *startup) Check() error {
	if r.Mode() == ModeOff {
		return nil
	}

	var anySet bool
	for _, kind := range []proxmox.Kind{proxmox.KindQemu, proxmox.KindLXC} {
		switch up := r.delayFor(kind); {
		case up == 0:
			// Diese Gastart ist bewusst ausgenommen.
		case up < time.Second:
			return fmt.Errorf("up für %s ist zu klein: der abstand muss mindestens 1s betragen", kind)
		default:
			anySet = true
		}
	}
	if !anySet {
		return errors.New("up fehlt: ohne abstand für wenigstens eine gastart täte die regel nichts")
	}
	return nil
}

func (r *startup) Apply(plan *Plan) {
	up := r.delayFor(plan.Kind)
	if up == 0 {
		plan.Skip(r, "startup", "für "+string(plan.Kind)+" ist kein abstand eingestellt")
		return
	}
	if onboot, _ := plan.Value("onboot"); onboot != "1" {
		plan.Skip(r, "startup", "gast startet nicht automatisch mit")
		return
	}
	plan.SetProperty(r, "startup", "up", strconv.Itoa(int(up.Seconds())))
}
