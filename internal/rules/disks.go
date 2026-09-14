package rules

import (
	"strconv"
	"strings"

	"pve-optimizer/internal/limits"
)

// PoolFilter schränkt eine Regel auf bestimmte Speicherpools ein. Leer
// heißt: alle. Eingebettet in die Regeln, die nur für einen Teil der Pools
// sinnvoll sind — Discard und das SSD-Kennzeichen gehören auf Flash, nicht
// auf eine Reihe drehender Platten.
type PoolFilter struct {
	Pools []string `yaml:"pools"`
}

func (p PoolFilter) covers(storage string) bool {
	if len(p.Pools) == 0 {
		return true
	}
	for _, name := range p.Pools {
		if name == storage {
			return true
		}
	}
	return false
}

// bus ist der Anschluss einer Platte ohne die Nummer, aus "scsi0" also
// "scsi".
func bus(disk *limits.Disk) string {
	return strings.TrimRight(disk.Key, "0123456789")
}

// ioLimits ergänzt die fehlenden IO-Begrenzungen einer Platte aus dem
// Profil ihres Speicherpools.
type ioLimits struct {
	Base `yaml:",inline"`
}

func (r *ioLimits) Apply(plan *Plan) {
	for _, disk := range plan.Disks() {
		profile, source := plan.Profile(disk.Storage())

		missing := limits.Missing(*disk, profile)
		if len(missing) == 0 {
			continue
		}

		options := make(map[string]string, len(missing))
		for key, value := range missing {
			options[key] = strconv.Itoa(value)
		}
		plan.AddDiskOptions(r, disk, options, "profil "+source)
	}
}

// discard gibt gelöschte Blöcke an den Speicher zurück. Ohne das wächst
// eine dünn bereitgestellte Platte immer weiter, auch wenn im Gast
// aufgeräumt wird.
type discard struct {
	Base       `yaml:",inline"`
	PoolFilter `yaml:",inline"`
}

func (r *discard) Apply(plan *Plan) {
	for _, disk := range plan.Disks() {
		if !r.covers(disk.Storage()) {
			continue
		}
		plan.AddDiskOptions(r, disk, map[string]string{"discard": "on"}, "")
	}
}

// ssd meldet dem Gast, dass die Platte nicht dreht. Linux und Windows
// schalten daraufhin die Optimierungen für Magnetplatten ab und schicken
// von sich aus TRIM-Befehle.
type ssd struct {
	Base       `yaml:",inline"`
	PoolFilter `yaml:",inline"`
}

func (r *ssd) Apply(plan *Plan) {
	for _, disk := range plan.Disks() {
		if !r.covers(disk.Storage()) {
			continue
		}
		// virtio-blk kennt das Kennzeichen nicht; Proxmox weist es dort ab.
		if bus(disk) == "virtio" {
			plan.Skip(r, disk.Key, "virtio-blk kennt kein ssd-kennzeichen")
			continue
		}
		plan.AddDiskOptions(r, disk, map[string]string{"ssd": "1"}, "")
	}
}

// iothread gibt der Platte einen eigenen Thread, statt sie über den
// Haupt-Thread von QEMU laufen zu lassen. Das entlastet vor allem VMs mit
// mehreren Platten unter Last.
type iothread struct {
	Base `yaml:",inline"`
}

func (r *iothread) Apply(plan *Plan) {
	controller, _ := plan.Value("scsihw")

	for _, disk := range plan.Disks() {
		switch bus(disk) {
		case "virtio":
		case "scsi":
			// Am SCSI-Bus geht das nur mit virtio-scsi-single, weil jede
			// Platte dort einen eigenen Controller bekommt. Den Typ
			// umzustellen ist ein Eingriff am laufenden Gast — manches
			// Windows bootet danach nicht mehr. Das bleibt Handarbeit.
			if controller != "virtio-scsi-single" {
				plan.Skip(r, disk.Key, "scsihw ist nicht virtio-scsi-single")
				continue
			}
		default:
			plan.Skip(r, disk.Key, bus(disk)+" kennt keinen eigenen io-thread")
			continue
		}
		plan.AddDiskOptions(r, disk, map[string]string{"iothread": "1"}, "")
	}
}
