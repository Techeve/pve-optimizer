package rules

import (
	"errors"
	"strconv"
	"time"
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
// fahren alle automatisch startenden VMs gleichzeitig hoch und erzeugen
// genau die Lastspitze, gegen die die IO-Begrenzung sonst arbeitet.
//
// Angefasst werden nur VMs, die ohnehin automatisch mitstarten. Ob eine
// VM mitstarten soll, ist eine Entscheidung des Betreibers und keine
// vergessene Einstellung — "onboot" fehlt bei einer VM, die bewusst von
// Hand gestartet wird, genauso wie bei einer, an die niemand gedacht hat.
type startup struct {
	Base `yaml:",inline"`

	// Up ist der Abstand, den Proxmox nach dieser VM einhält, bevor die
	// nächste startet.
	Up time.Duration `yaml:"up"`
}

func (r *startup) Check() error {
	if r.Mode() != ModeOff && r.Up < time.Second {
		return errors.New("up fehlt oder ist zu klein: der abstand muss mindestens 1s betragen")
	}
	return nil
}

func (r *startup) Apply(plan *Plan) {
	if onboot, _ := plan.Value("onboot"); onboot != "1" {
		plan.Skip(r, "startup", "vm startet nicht automatisch mit")
		return
	}
	plan.SetProperty(r, "startup", "up", strconv.Itoa(int(r.Up.Seconds())))
}
