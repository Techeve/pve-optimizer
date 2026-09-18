package advice

import (
	"context"
	"fmt"

	"pve-optimizer/internal/proxmox"
)

// backupCoverage nennt die Gäste, die kein Sicherungsauftrag erfasst.
//
// Der häufigste Weg dorthin ist harmlos und deshalb tückisch: Ein
// Sicherungsauftrag mit fester VMID-Liste erfasst neue Gäste nicht. Wer
// eine VM anlegt und den Auftrag nicht anfasst, hat sie schlicht nicht
// gesichert — und merkt es erst, wenn er sie zurückholen will.
//
// Eingetragen wird hier nichts. Welcher Gast in welchen Auftrag gehört,
// mit welchem Zeitplan und welcher Aufbewahrung, ist eine Entscheidung des
// Betreibers.
type backupCoverage struct {
	Base `yaml:",inline"`

	// Ignore sind Gäste, die bewusst keine Sicherung brauchen — Testgäste,
	// Wegwerf-VMs, Gäste mit eigener Sicherung im Gast.
	Ignore []int `yaml:"ignore"`
}

func (c *backupCoverage) Run(ctx context.Context, cluster Cluster) ([]Finding, error) {
	offen, err := cluster.NotBackedUp(ctx)
	if err != nil {
		return nil, err
	}
	guests, err := cluster.ListGuests(ctx)
	if err != nil {
		return nil, err
	}

	// Die Antwort von Proxmox nennt keinen Node. Den holen wir aus der
	// Gastliste dazu, damit im Bericht steht, wo der Gast liegt.
	nodes := make(map[int]proxmox.Guest, len(guests))
	for _, guest := range guests {
		nodes[guest.VMID] = guest
	}

	skip := ignored(c.Ignore)
	var findings []Finding
	for _, guest := range offen {
		if skip[guest.VMID] {
			continue
		}
		findings = append(findings, Finding{
			Check:   c.Name(),
			Subject: describe(guest, nodes[guest.VMID]),
			Why:     "Kein Sicherungsauftrag erfasst diesen Gast. Geht der Speicher verloren, ist er weg.",
			Action: "Unter Rechenzentrum → Backup einem Auftrag hinzufügen." +
				" Braucht der Gast bewusst keine Sicherung, gehört seine VMID unter advice.backup_coverage.ignore.",
		})
	}
	return append(findings, c.staleIgnores(guests)...), nil
}

// staleIgnores meldet Nummern auf der Ignore-Liste, zu denen es keinen Gast
// mehr gibt. Proxmox vergibt gelöschte VMIDs wieder — eine Liste, die alte
// Nummern mitschleppt, deckt sonst irgendwann stillschweigend einen neuen
// Gast ab, an den niemand gedacht hat.
func (c *backupCoverage) staleIgnores(guests []proxmox.Guest) []Finding {
	if len(c.Ignore) == 0 {
		return nil
	}

	vorhanden := make(map[int]bool, len(guests))
	for _, guest := range guests {
		vorhanden[guest.VMID] = true
	}

	var findings []Finding
	for _, vmid := range sortedVMIDs(c.Ignore) {
		if vorhanden[vmid] {
			continue
		}
		findings = append(findings, Finding{
			Check:   c.Name(),
			Subject: fmt.Sprintf("VMID %d steht auf der Ignore-Liste, es gibt sie aber nicht mehr", vmid),
			Why: "Proxmox vergibt gelöschte Nummern wieder. Der Eintrag würde dann einen" +
				" neuen Gast von der Prüfung ausnehmen, ohne dass es jemand bemerkt.",
			Action: "Eintrag unter advice.backup_coverage.ignore entfernen.",
		})
	}
	return findings
}

// describe benennt den Gast so, wie ein Mensch ihn sucht: Art, Nummer,
// Name und Node. Was die Gastliste nicht hergibt, bleibt weg.
func describe(guest, known proxmox.Guest) string {
	art := "VM"
	if guest.Kind == proxmox.KindLXC {
		art = "Container"
	}

	text := fmt.Sprintf("%s %d", art, guest.VMID)
	if name := firstNonEmpty(guest.Name, known.Name); name != "" {
		text += " (" + name + ")"
	}
	if known.Node != "" {
		text += " auf " + known.Node
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
