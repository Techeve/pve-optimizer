package rules

import (
	"sort"
	"strings"

	"pve-optimizer/internal/limits"
)

// Status sagt, was aus einem Vorschlag geworden ist.
type Status string

const (
	// StatusWritten geht in die VM-Konfiguration.
	StatusWritten Status = "geschrieben"
	// StatusReported wäre zu ergänzen, bleibt aber ungeschrieben — die
	// Regel steht auf "report".
	StatusReported Status = "gemeldet"
	// StatusSkipped nennt einen Punkt, an dem die Regel nicht greifen
	// konnte, samt Grund.
	StatusSkipped Status = "übersprungen"
)

// Note ist ein Eintrag fürs Protokoll.
type Note struct {
	Rule    string // Name der Regel
	Key     string // Feld der VM-Konfiguration, etwa "scsi0" oder "agent"
	Change  string // was ergänzt wird, etwa "discard=on"
	Context string // Herkunft oder Grund, etwa "profil local-pool"
	Status  Status
}

// Profiles liefert das Drosselungsprofil eines Speicherpools samt der
// Fundstelle, aus der es stammt.
type Profiles func(storage string) (limits.Profile, string)

// Plan ist der Entwurf für eine VM. Die Regeln tragen nacheinander ihre
// Ergänzungen ein; geschrieben wird am Ende einmal.
//
// Der Grundsatz steckt hier und nicht in den einzelnen Regeln: Was bereits
// gesetzt ist, wird nicht angerührt. Der Dienst ergänzt nur.
type Plan struct {
	Node string
	VMID int

	config   map[string]string
	disks    []*limits.Disk
	profiles Profiles

	fields map[string]string
	notes  []Note
}

// NewPlan liest die Konfiguration einer VM ein.
func NewPlan(node string, vmid int, config map[string]string, profiles Profiles) *Plan {
	plan := &Plan{
		Node:     node,
		VMID:     vmid,
		config:   config,
		profiles: profiles,
		fields:   map[string]string{},
	}

	for key, value := range config {
		disk, ok := limits.ParseDisk(key, value)
		if !ok || disk.IsCDROM() {
			continue
		}
		plan.disks = append(plan.disks, &disk)
	}
	sort.Slice(plan.disks, func(i, j int) bool { return plan.disks[i].Key < plan.disks[j].Key })
	return plan
}

// Disks sind die Platten der VM, nach Bus sortiert. CD-ROM-Laufwerke sowie
// efidisk, tpmstate und unused* sind nicht dabei.
func (p *Plan) Disks() []*limits.Disk { return p.disks }

// Value liefert einen Wert aus der VM-Konfiguration, etwa "scsihw".
func (p *Plan) Value(key string) (string, bool) {
	value, found := p.config[key]
	return value, found
}

// Profile liefert das Drosselungsprofil eines Speicherpools auf dem Node,
// für den geplant wird.
func (p *Plan) Profile(storage string) (limits.Profile, string) {
	return p.profiles(storage)
}

// AddDiskOptions ergänzt an einer Platte die Optionen, die dort noch
// fehlen. context erscheint im Protokoll — bei der IO-Begrenzung etwa das
// Profil, aus dem die Werte stammen.
func (p *Plan) AddDiskOptions(rule Rule, disk *limits.Disk, options map[string]string, context string) {
	missing := map[string]string{}
	for name, value := range options {
		if _, set := disk.Options[name]; !set {
			missing[name] = value
		}
	}
	if len(missing) == 0 {
		return
	}

	status := p.record(rule, disk.Key, format(missing), context)
	if status != StatusWritten {
		return
	}
	for name, value := range missing {
		disk.Options[name] = value
	}
	p.fields[disk.Key] = disk.Render()
}

// SetValue setzt ein Feld der VM-Konfiguration, wenn es noch fehlt.
func (p *Plan) SetValue(rule Rule, key, value string) {
	if _, set := p.config[key]; set {
		return
	}
	if p.record(rule, key, key+"="+value, "") != StatusWritten {
		return
	}
	p.config[key] = value
	p.fields[key] = value
}

// SetProperty ergänzt eine Option in einem Feld, das mehrere Werte trägt —
// "startup" etwa lautet "order=2,up=30". Ein bereits gesetzter Name bleibt
// unangetastet.
func (p *Plan) SetProperty(rule Rule, key, name, value string) {
	current := p.config[key]
	parts := splitProperty(current)
	for _, part := range parts {
		if existing, _, _ := strings.Cut(part, "="); existing == name {
			return
		}
	}

	if p.record(rule, key, name+"="+value, "") != StatusWritten {
		return
	}
	updated := strings.Join(append(parts, name+"="+value), ",")
	p.config[key] = updated
	p.fields[key] = updated
}

func splitProperty(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

// Skip vermerkt, dass eine Regel an dieser Stelle nicht greifen konnte.
func (p *Plan) Skip(rule Rule, key, reason string) {
	p.notes = append(p.notes, Note{
		Rule: rule.Name(), Key: key, Context: reason, Status: StatusSkipped,
	})
}

// record hält einen Vorschlag fest und meldet, ob er geschrieben wird.
func (p *Plan) record(rule Rule, key, change, context string) Status {
	status := StatusReported
	if rule.Mode() == ModeEnforce {
		status = StatusWritten
	}
	p.notes = append(p.notes, Note{
		Rule: rule.Name(), Key: key, Change: change, Context: context, Status: status,
	})
	return status
}

// Fields sind die Felder, die in die VM-Konfiguration geschrieben werden.
func (p *Plan) Fields() map[string]string { return p.fields }

// Notes sind alle Vermerke der Regeln, in der Reihenfolge ihres Auftretens.
func (p *Plan) Notes() []Note { return p.notes }

// format bringt die Ergänzungen in eine feste Reihenfolge — Maps haben
// keine, und ein Protokoll, das bei jedem Lauf anders aussieht, ist
// schlecht zu lesen.
func format(options map[string]string) string {
	parts := make([]string, 0, len(options))
	for name, value := range options {
		parts = append(parts, name+"="+value)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
