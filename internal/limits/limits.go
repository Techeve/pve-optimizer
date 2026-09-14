// Package limits liest Proxmox-Disk-Einträge und ermittelt, welche
// IO-Begrenzungen einer Platte noch fehlen.
package limits

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Parameterfamilien, wie Proxmox sie kennt. Durchsatz und IOPS lassen sich
// jeweils entweder gemeinsam für beide Richtungen begrenzen oder getrennt
// nach Lesen und Schreiben — beides zusammen lehnt QEMU ab.
//
// Achtung bei der Benennung: Die Burst-Dauer heißt beim Durchsatz
// "bps_..._max_length", nicht "mbps_...". Ohne sie bleibt ein gesetztes
// "_max" praktisch wirkungslos, weil QEMU dann nur eine Sekunde burstet.
var (
	throughputCombined = []string{"mbps", "mbps_max", "bps_max_length"}
	throughputSplit    = []string{
		"mbps_rd", "mbps_wr", "mbps_rd_max", "mbps_wr_max",
		"bps_rd_max_length", "bps_wr_max_length",
	}
	iopsCombined = []string{"iops", "iops_max", "iops_max_length"}
	iopsSplit    = []string{
		"iops_rd", "iops_wr", "iops_rd_max", "iops_wr_max",
		"iops_rd_max_length", "iops_wr_max_length",
	}
)

// Keys sind alle Parameter, die der Dienst setzen kann. Die Reihenfolge
// bestimmt, wie sie in die VM-Config geschrieben werden.
var Keys = concat(throughputCombined, throughputSplit, iopsCombined, iopsSplit)

// Paare, die sich gegenseitig ausschließen.
var exclusivePairs = []struct{ combined, split []string }{
	{throughputCombined, throughputSplit},
	{iopsCombined, iopsSplit},
}

func concat(lists ...[]string) []string {
	var all []string
	for _, list := range lists {
		all = append(all, list...)
	}
	return all
}

// Profile sind die Zielwerte eines Speicherpools. Ein Wert von 0 bedeutet
// "nicht setzen" — so lassen sich einzelne Begrenzungen bewusst offenlassen.
type Profile map[string]int

// diskKey erkennt die Bus-Namen, an denen echte Platten hängen. efidisk,
// tpmstate und unused* sind bewusst nicht dabei: Sie tragen keine Nutzlast
// und Proxmox akzeptiert dort keine Drosselung.
var diskKey = regexp.MustCompile(`^(scsi|virtio|sata|ide)\d+$`)

// Disk ist ein geparster Eintrag aus der VM-Config, etwa
// "local-pool:vm-100-disk-0,size=32G,iothread=1".
type Disk struct {
	Key     string            // Bus-Name, z. B. "scsi0"
	Volume  string            // "local-pool:vm-100-disk-0"
	Options map[string]string // alle weiteren key=value-Paare
}

// Storage liefert den Speicherpool, auf dem die Platte liegt.
func (d Disk) Storage() string {
	name, _, found := strings.Cut(d.Volume, ":")
	if !found {
		return ""
	}
	return name
}

// IsCDROM meldet optische Laufwerke, die nicht gedrosselt werden.
func (d Disk) IsCDROM() bool {
	return d.Options["media"] == "cdrom"
}

// ParseDisk zerlegt einen Config-Eintrag. Der zweite Rückgabewert ist false,
// wenn der Key keine drosselbare Platte bezeichnet.
func ParseDisk(key, value string) (Disk, bool) {
	if !diskKey.MatchString(key) {
		return Disk{}, false
	}

	parts := strings.Split(value, ",")
	disk := Disk{Key: key, Volume: parts[0], Options: map[string]string{}}
	for _, part := range parts[1:] {
		name, val, found := strings.Cut(part, "=")
		if !found {
			// Flags ohne Wert (etwa "backup") als gesetzt vermerken.
			disk.Options[part] = ""
			continue
		}
		disk.Options[name] = val
	}
	return disk, true
}

// Missing liefert die Begrenzungen aus dem Profil, die an der Platte noch
// fehlen. Bereits gesetzte Werte bleiben unangetastet — der Dienst ergänzt
// nur, er überschreibt keine bewusst abweichenden Einstellungen.
//
// Familien, deren Gegenstück an der Platte schon gesetzt ist, bleiben außen
// vor: Ein kombiniertes "mbps" neben einem ergänzten "mbps_rd" würde QEMU
// zurückweisen und die ganze Änderung scheitern lassen.
func Missing(disk Disk, profile Profile) map[string]int {
	blocked := blockedKeys(disk)

	missing := map[string]int{}
	for _, key := range Keys {
		target := profile[key]
		if target <= 0 || blocked[key] {
			continue
		}
		if _, alreadySet := disk.Options[key]; alreadySet {
			continue
		}
		missing[key] = target
	}
	return missing
}

// blockedKeys sammelt die Parameter, die wegen einer bereits an der Platte
// gesetzten Gegenfamilie nicht ergänzt werden dürfen.
func blockedKeys(disk Disk) map[string]bool {
	blocked := map[string]bool{}
	block := func(keys []string) {
		for _, key := range keys {
			blocked[key] = true
		}
	}

	for _, pair := range exclusivePairs {
		if anySet(disk.Options, pair.combined) {
			block(pair.split)
		}
		if anySet(disk.Options, pair.split) {
			block(pair.combined)
		}
	}
	return blocked
}

// CheckProfile weist Profile zurück, die ein kombiniertes Limit mit
// getrennten Lese-/Schreibwerten mischen — QEMU nimmt beides zusammen
// nicht an, und der Dienst würde bei jeder Platte scheitern.
func CheckProfile(profile Profile) error {
	options := make(map[string]string, len(profile))
	for key, value := range profile {
		if value > 0 {
			options[key] = ""
		}
	}

	for _, pair := range exclusivePairs {
		if anySet(options, pair.combined) && anySet(options, pair.split) {
			return fmt.Errorf("%s und %s lassen sich nicht zusammen setzen",
				strings.Join(pair.combined, "/"), strings.Join(pair.split, "/"))
		}
	}
	return nil
}

func anySet[V any](options map[string]V, keys []string) bool {
	for _, key := range keys {
		if _, found := options[key]; found {
			return true
		}
	}
	return false
}

// Render baut den Config-Wert der Platte — den vollständigen String, den
// Proxmox für diesen Bus erwartet. Mehrere Regeln ergänzen nacheinander
// Optionen an derselben Platte; geschrieben wird am Ende einmal.
func (d Disk) Render() string {
	parts := []string{d.Volume}
	for name, value := range d.Options {
		if value == "" {
			// Flags ohne Wert, etwa "backup".
			parts = append(parts, name)
			continue
		}
		parts = append(parts, name+"="+value)
	}

	// Die Optionen stammen aus einer Map und haben damit keine stabile
	// Reihenfolge. Proxmox ist das egal, für lesbare Diffs und Tests aber
	// nicht: Volume zuerst, danach alles Weitere sortiert.
	sort.Strings(parts[1:])
	return strings.Join(parts, ",")
}
