package advice

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"pve-optimizer/internal/proxmox"
)

// zfsHealth sieht nach, ob ZFS an einem Pool etwas zu beanstanden hat.
//
// Das ist auf einem Node ohne ECC-Speicher oft das einzige Warnsignal, das
// überhaupt kommt: ZFS prüft jeden gelesenen Block gegen seine Prüfsumme
// und merkt damit Verfälschungen, die weder SMART noch die
// Machine-Check-Zähler je sehen.
type zfsHealth struct {
	Base `yaml:",inline"`
}

func (c *zfsHealth) Run(ctx context.Context, cluster Cluster) ([]Finding, error) {
	nodes, err := cluster.Nodes(ctx)
	if err != nil {
		return nil, err
	}

	var findings []Finding
	for _, node := range nodes {
		// Ein Node, der offline steht, liefert keine Werte. Ihn als
		// fehlerfrei zu melden wäre falsch, ihn als kaputt zu melden auch.
		if !node.Online() {
			continue
		}
		found, err := c.checkNode(ctx, cluster, node.Name)
		if err != nil {
			return nil, err
		}
		findings = append(findings, found...)
	}
	return findings, nil
}

func (c *zfsHealth) checkNode(ctx context.Context, cluster Cluster, node string) ([]Finding, error) {
	pools, err := cluster.ZFSPools(ctx, node)
	if err != nil {
		return nil, err
	}

	var findings []Finding
	var mitPruefsummenfehlern []string

	for _, pool := range pools {
		status, err := cluster.ZFSPoolStatus(ctx, node, pool.Name)
		if err != nil {
			return nil, err
		}
		if status.Healthy() {
			continue
		}

		findings = append(findings, Finding{
			Check:   c.Name(),
			Subject: fmt.Sprintf("Pool %s auf %s: %s", pool.Name, node, beanstandung(status)),
			Why: "ZFS hat beim Lesen etwas gefunden, das nicht zu seiner Prüfsumme passt," +
				" oder ein Gerät als gestört eingestuft. Unbehandelt wächst das.",
			Action: "Auf dem Node \"zpool status -v\" ansehen. Betroffene Dateien aus der" +
				" Sicherung zurückholen, danach \"zpool clear\" und einen Scrub — erst der" +
				" zeigt, ob es bei diesem einen Mal blieb.",
		})

		if len(status.FaultyDevices()) > 0 {
			mitPruefsummenfehlern = append(mitPruefsummenfehlern, pool.Name)
		}
	}

	// Der Querbefund, der die eigentliche Ursache verrät: Zwei
	// unabhängige Laufwerke fangen nicht gleichzeitig an, Daten zu
	// verfälschen. Passiert es doch, liegt die Ursache oberhalb der
	// Laufwerke — im Speicher, im Speichercontroller oder auf dem
	// PCIe-Pfad. Auf einem System ohne ECC merkt das sonst niemand.
	if len(mitPruefsummenfehlern) > 1 {
		findings = append(findings, Finding{
			Check: c.Name(),
			Subject: fmt.Sprintf("%s: Fehlerzähler auf mehreren Pools (%s)",
				node, strings.Join(mitPruefsummenfehlern, ", ")),
			Why: "Zwei unabhängige Laufwerke fangen nicht gleichzeitig an, Daten zu verfälschen." +
				" Das deutet auf eine Ursache oberhalb der Laufwerke hin — Arbeitsspeicher," +
				" Speichercontroller oder PCIe-Pfad.",
			Action: "SMART der Laufwerke prüfen: Melden sie null Medienfehler, sind sie" +
				" wahrscheinlich unschuldig. Ohne ECC-Speicher ist ein Speicherfehler dann der" +
				" naheliegende Verdacht — ein memtest findet ihn oft nicht, weil er den" +
				" Speichercontroller unter gemischter Last nicht nachbildet.",
		})
	}
	return findings, nil
}

// beanstandung fasst zusammen, was ZFS bemängelt.
func beanstandung(status proxmox.ZFSStatus) string {
	var teile []string
	if status.State != "ONLINE" {
		teile = append(teile, "Zustand "+status.State)
	}
	if status.Errors != "" && status.Errors != "No known data errors" {
		teile = append(teile, status.Errors)
	}
	if faulty := status.FaultyDevices(); len(faulty) > 0 {
		var namen []string
		for _, vdev := range faulty {
			namen = append(namen, fmt.Sprintf("%s (L%d/S%d/P%d)",
				kurzerGeraetename(vdev.Name), vdev.Read, vdev.Write, vdev.Cksum))
		}
		teile = append(teile, "Fehlerzähler an "+strings.Join(namen, ", "))
	}
	if len(teile) == 0 {
		return "auffällig"
	}
	return strings.Join(teile, "; ")
}

// kurzerGeraetename kürzt die langen Pfade aus /dev/disk/by-id auf das,
// was ein Mensch wiedererkennt.
func kurzerGeraetename(name string) string {
	if at := strings.LastIndex(name, "/"); at >= 0 {
		return name[at+1:]
	}
	return name
}

// zfsRedundancy nennt Pools, die einen Ausfall nicht ausgleichen können.
//
// Ohne zweite Kopie erkennt ZFS einen Prüfsummenfehler zwar, kann ihn aber
// nicht beheben — die betroffene Datei ist dann verloren und muss aus der
// Sicherung zurück.
type zfsRedundancy struct {
	Base `yaml:",inline"`
}

func (c *zfsRedundancy) Run(ctx context.Context, cluster Cluster) ([]Finding, error) {
	nodes, err := cluster.Nodes(ctx)
	if err != nil {
		return nil, err
	}

	var findings []Finding
	for _, node := range nodes {
		if !node.Online() {
			continue
		}
		pools, err := cluster.ZFSPools(ctx, node.Name)
		if err != nil {
			return nil, err
		}
		for _, pool := range pools {
			status, err := cluster.ZFSPoolStatus(ctx, node.Name, pool.Name)
			if err != nil {
				return nil, err
			}
			if status.Redundant() {
				continue
			}
			findings = append(findings, Finding{
				Check:   c.Name(),
				Subject: fmt.Sprintf("Pool %s auf %s hat keine Redundanz", pool.Name, node.Name),
				Why: "ZFS erkennt einen Prüfsummenfehler, kann ihn ohne zweite Kopie aber nicht" +
					" beheben. Die betroffene Datei ist dann verloren.",
				Action: "Das lässt sich nachträglich nicht ändern, ein Pool wächst nicht zum" +
					" Spiegel. Wichtig ist deshalb die Sicherung — und ein regelmäßiger Scrub," +
					" damit ein Fehler auffällt, solange die Sicherung noch gut ist.",
			})
		}
	}
	return findings, nil
}

// memoryOvercommit meldet Nodes, deren laufende Gäste zusammen mehr
// Hauptspeicher zugesagt bekommen haben, als dem Node bleibt.
//
// Der Host braucht selbst welchen: ZFS legt seinen Lesezwischenspeicher
// an, eine Sicherung braucht Puffer, der Kernel ohnehin. Wird es eng,
// fängt der Node an auszulagern — und wenn er nicht kann, greift der
// OOM-Killer sich einen Gast.
type memoryOvercommit struct {
	Base `yaml:",inline"`

	// MaxPercent ist der Anteil des Hauptspeichers, den die laufenden
	// Gäste zusammen belegen dürfen, bevor die Prüfung anschlägt.
	MaxPercent int `yaml:"max_percent"`
}

func (c *memoryOvercommit) Validate() error {
	if err := c.Base.Validate(); err != nil {
		return err
	}
	if c.MaxPercent < 1 || c.MaxPercent > 100 {
		return fmt.Errorf("max_percent muss zwischen 1 und 100 liegen, ist %d", c.MaxPercent)
	}
	return nil
}

func (c *memoryOvercommit) Run(ctx context.Context, cluster Cluster) ([]Finding, error) {
	nodes, err := cluster.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	guests, err := cluster.ListGuests(ctx)
	if err != nil {
		return nil, err
	}

	zugesagt := map[string]int64{}
	for _, guest := range guests {
		if guest.Running() {
			zugesagt[guest.Node] += guest.MaxMem
		}
	}

	var namen []string
	for _, node := range nodes {
		if node.Online() && node.MaxMem > 0 {
			namen = append(namen, node.Name)
		}
	}
	sort.Strings(namen)

	byName := map[string]proxmox.Node{}
	for _, node := range nodes {
		byName[node.Name] = node
	}

	var findings []Finding
	for _, name := range namen {
		node := byName[name]
		anteil := int(zugesagt[name] * 100 / node.MaxMem)
		if anteil <= c.MaxPercent {
			continue
		}
		findings = append(findings, Finding{
			Check: c.Name(),
			Subject: fmt.Sprintf("%s: laufende Gäste haben %s von %s zugesagt (%d %%)",
				name, gib(zugesagt[name]), gib(node.MaxMem), anteil),
			Why: "Der Host braucht selbst Hauptspeicher — ZFS für seinen Lesezwischenspeicher," +
				" eine Sicherung für ihre Puffer, der Kernel ohnehin. Wird es eng, lagert der" +
				" Node aus; kann er das nicht, greift sich der OOM-Killer einen Gast.",
			Action: fmt.Sprintf("Gästen Speicher wegnehmen oder welche auf einen anderen Node"+
				" verschieben. Die Grenze steht auf %d %% und lässt sich unter"+
				" advice.memory_overcommit.max_percent verschieben.", c.MaxPercent),
		})
	}
	return findings, nil
}

// gib schreibt Bytes so, wie ein Mensch sie liest.
func gib(bytes int64) string {
	return fmt.Sprintf("%.1f GiB", float64(bytes)/(1024*1024*1024))
}
