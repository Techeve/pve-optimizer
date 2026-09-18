package advice

import (
	"context"
	"fmt"
	"strings"

	"pve-optimizer/internal/proxmox"
)

// bandwidthLimits prüft die Bandbreitengrenzen des Rechenzentrums.
//
// Ohne Grenze zieht ein einzelner Vorgang die Leitung leer. Das trifft
// nicht nur die Gäste: Liegt der Cluster-Verkehr auf derselben Leitung —
// und ohne eigenen Corosync-Link tut er das —, kann ein Wiederherstellen
// die Cluster-Kommunikation abschnüren. Im schlimmsten Fall verlieren die
// Nodes darüber ihr Quorum.
type bandwidthLimits struct {
	Base `yaml:",inline"`
}

func (c *bandwidthLimits) Run(ctx context.Context, cluster Cluster) ([]Finding, error) {
	options, err := cluster.Options(ctx)
	if err != nil {
		return nil, err
	}

	// Ist gar nichts gesetzt, ist ein Sammelbefund ehrlicher als vier
	// gleichlautende Zeilen.
	if len(options.BandwidthLimits) == 0 {
		return []Finding{{
			Check:   c.Name(),
			Subject: "Es ist keine einzige Bandbreitengrenze gesetzt",
			Why:     ungebremst,
			Action:  einstellweg,
		}}, nil
	}

	var offen []string
	for _, operation := range proxmox.BandwidthOperations {
		if !options.HasBandwidthLimit(operation) {
			offen = append(offen, operation)
		}
	}
	if len(offen) == 0 {
		return nil, nil
	}
	return []Finding{{
		Check:   c.Name(),
		Subject: "Ohne Bandbreitengrenze: " + strings.Join(offen, ", "),
		Why:     ungebremst,
		Action:  einstellweg,
	}}, nil
}

const (
	ungebremst = "Ein ungebremster Vorgang zieht die Leitung leer." +
		" Teilt sich der Cluster-Verkehr dieselbe Leitung, kann das Corosync abschnüren —" +
		" im schlimmsten Fall verlieren die Nodes ihr Quorum."
	einstellweg = "Rechenzentrum → Optionen → Bandbreitenbegrenzungen." +
		" Achtung bei der Einheit: Proxmox rechnet dort in KiB/s, nicht in MB/s wie an Platten und Netzwerkkarten." +
		" \"default\" deckt alles ab, was keine eigene Grenze hat."
)

// replicationRate nennt Replikationsaufträge ohne Ratenbegrenzung.
//
// Eine Replikation überträgt die Unterschiede seit dem letzten Lauf. Nach
// einer größeren Änderung im Gast — einem Update, einem Import — ist das
// schlagartig viel, und der Auftrag nimmt sich, was die Leitung hergibt.
type replicationRate struct {
	Base `yaml:",inline"`
}

func (c *replicationRate) Run(ctx context.Context, cluster Cluster) ([]Finding, error) {
	jobs, err := cluster.ReplicationJobs(ctx)
	if err != nil {
		return nil, err
	}

	var findings []Finding
	for _, job := range jobs {
		if job.Limited() {
			continue
		}
		findings = append(findings, Finding{
			Check:   c.Name(),
			Subject: fmt.Sprintf("Replikationsauftrag %s (%s → %s) läuft ungebremst", job.ID, job.Source, job.Target),
			Why: "Nach einer größeren Änderung im Gast überträgt der Auftrag schlagartig viel" +
				" und nimmt sich, was die Leitung hergibt.",
			Action: "Rechenzentrum → Replikation → Auftrag bearbeiten → Rate." +
				" Die Angabe ist dort MB/s.",
		})
	}
	return findings, nil
}
