// Package limits liest Proxmox-Disk-Einträge und ermittelt, welche
// IO-Begrenzungen einer Platte noch fehlen.
package limits

import (
	"fmt"
	"regexp"
	"strings"
)

// Keys, die Proxmox für die Drosselung einer Platte kennt. Die Reihenfolge
// bestimmt, wie die Parameter an die VM-Config geschrieben werden.
var Keys = []string{
	"mbps_rd", "mbps_wr", "mbps_rd_max", "mbps_wr_max",
	"iops_rd", "iops_wr", "iops_rd_max", "iops_wr_max",
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
func Missing(disk Disk, profile Profile) map[string]int {
	missing := map[string]int{}
	for _, key := range Keys {
		target := profile[key]
		if target <= 0 {
			continue
		}
		if _, alreadySet := disk.Options[key]; alreadySet {
			continue
		}
		missing[key] = target
	}
	return missing
}

// Apply baut den Config-Wert für die Platte samt der ergänzten Begrenzungen.
// Das Ergebnis ist der vollständige String, den Proxmox für diesen Bus
// erwartet.
func Apply(disk Disk, additions map[string]int) string {
	parts := []string{disk.Volume}

	for name, value := range disk.Options {
		if value == "" {
			parts = append(parts, name)
			continue
		}
		parts = append(parts, name+"="+value)
	}

	for _, key := range Keys {
		if value, ok := additions[key]; ok {
			parts = append(parts, fmt.Sprintf("%s=%d", key, value))
		}
	}

	// Die Optionen stammen aus einer Map und haben damit keine stabile
	// Reihenfolge. Proxmox ist das egal, für lesbare Diffs und Tests aber
	// nicht: Volume zuerst, danach alles Weitere sortiert.
	sortTail(parts)
	return strings.Join(parts, ",")
}

func sortTail(parts []string) {
	tail := parts[1:]
	for i := 1; i < len(tail); i++ {
		for j := i; j > 0 && tail[j] < tail[j-1]; j-- {
			tail[j], tail[j-1] = tail[j-1], tail[j]
		}
	}
}
