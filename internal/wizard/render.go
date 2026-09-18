package wizard

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pve-optimizer/internal/config"
	"pve-optimizer/internal/mode"
)

// render baut die Konfigurationsdatei. Bewusst als Text und nicht über
// einen YAML-Encoder: Die Kommentare sind die halbe Miete, und ein Encoder
// wirft sie weg.
func (w *Wizard) render(p plan, found umgebung) string {
	var out strings.Builder

	fmt.Fprintf(&out, "# pve-optimizer — vom Wizard erzeugt am %s auf %s\n",
		w.now().Format("2006-01-02"), w.node)
	out.WriteString("#\n")
	out.WriteString("# Jeder Schlüssel ist in config.example.yaml ausführlich erklärt.\n")
	out.WriteString("# Diese Datei darf von Hand nachgebessert werden.\n\n")

	out.WriteString("mode: local\n")
	out.WriteString("poll_interval: 30s\n")
	out.WriteString("dry_run: false\n")
	out.WriteString("state_file: /var/lib/pve-optimizer/state.json\n\n")

	out.WriteString("# Greift für jeden Speicher, der unten kein eigenes Profil hat.\n")
	out.WriteString("defaults:\n")
	fmt.Fprintf(&out, "  mbps_rd: %d\n", p.mbpsRd)
	fmt.Fprintf(&out, "  mbps_wr: %d\n", p.mbpsWr)
	out.WriteString("\n")

	renderPools(&out, found.pools)
	renderRules(&out, p)
	renderServices(&out, p)
	renderMail(&out, p)
	renderAdvice(&out, p)
	renderReport(&out, p, w.node)

	return out.String()
}

func renderPools(out *strings.Builder, pools []string) {
	if len(pools) == 0 {
		return
	}
	out.WriteString("# Auf diesem Cluster tragen diese Speicher Gastplatten:\n")
	for _, pool := range pools {
		fmt.Fprintf(out, "#   %s\n", pool)
	}
	out.WriteString("# Wer einen davon abweichend drosseln will, trägt ihn hier ein:\n")
	out.WriteString("#\n# pools:\n")
	fmt.Fprintf(out, "#   %s:\n#     mbps_rd: 500\n#     mbps_wr: 400\n\n", pools[0])
}

func renderRules(out *strings.Builder, p plan) {
	out.WriteString("rules:\n")
	for _, name := range ruleOrder {
		m, gesetzt := p.rules[name]
		if !gesetzt {
			continue
		}
		switch {
		case name == "startup" && m != mode.Off:
			fmt.Fprintf(out, "  startup:\n    mode: %s\n", m)
			fmt.Fprintf(out, "    vm:\n      up: %s\n", short(p.startupVM))
			fmt.Fprintf(out, "    lxc:\n      up: %s\n", short(p.startupLXC))
		case name == "net_rate" && m != mode.Off:
			out.WriteString("  # Die Einheit ist MB/s — eine Gigabit-Leitung sind 125.\n")
			fmt.Fprintf(out, "  net_rate:\n    mode: %s\n    rate: %s\n", m, trimFloat(p.netRate))
		default:
			fmt.Fprintf(out, "  %s: %s\n", name, m)
		}
	}
	out.WriteString("\n")
}

// ruleOrder hält die Reihenfolge des Katalogs fest. Eine Datei, die bei
// jedem Lauf anders sortiert ist, lässt sich nicht vergleichen.
var ruleOrder = []string{"io_limits", "discard", "ssd", "iothread", "guest_agent", "startup", "net_rate"}

func renderServices(out *strings.Builder, p plan) {
	if !p.monitor {
		return
	}
	out.WriteString("# Holt abgestürzte systemd-Dienste des Nodes wieder hoch. Proxmox\n")
	out.WriteString("# liefert seine Units ohne \"Restart=\" aus.\n")
	out.WriteString("services:\n  mode: enforce\n  units:\n    - pvestatd.service\n")
	fmt.Fprintf(out, "  restart_limit: %d\n  stable_after: 24h\n\n", p.monitorLimit)
}

func renderMail(out *strings.Builder, p plan) {
	if p.mailServer == "" {
		out.WriteString("# Kein Mailserver eingerichtet — Meldungen bleiben im Protokoll.\n")
		out.WriteString("# Zum Nachrüsten:\n#\n# mail:\n#   to: admin@example.com\n#   server: mail.example.com:25\n\n")
		return
	}
	out.WriteString("# Gilt für den ganzen Dienst. Verlangt der Server eine Anmeldung,\n")
	out.WriteString("# gehört das Passwort in PVE_OPTIMIZER_SMTP_PASSWORD, nicht hierher.\n")
	fmt.Fprintf(out, "mail:\n  to: %s\n  server: %s\n\n", p.mailTo, p.mailServer)
}

func renderAdvice(out *strings.Builder, p plan) {
	out.WriteString("# Prüfungen, die hinweisen und nichts ändern.\n")
	out.WriteString("advice:\n  backup_coverage:\n    mode: report\n")
	if len(p.ignore) == 0 {
		out.WriteString("    # Gäste, die bewusst keine Sicherung brauchen:\n    ignore: []\n")
	} else {
		out.WriteString("    # Diese Gäste brauchen bewusst keine Sicherung.\n")
		fmt.Fprintf(out, "    ignore: [%s]\n", joinVMIDs(p.ignore))
	}
	out.WriteString("  bandwidth_limits: report\n  replication_rate: report\n\n")
}

func renderReport(out *strings.Builder, p plan, node string) {
	if !p.report {
		return
	}
	out.WriteString("# Meldet in festem Abstand, was die Prüfungen gefunden haben.\n")
	out.WriteString("# \"node\" ist Pflicht: Sonst verschickt jede Instanz denselben Bericht.\n")
	fmt.Fprintf(out, "report:\n  mode: enforce\n  node: %s\n  every: %s\n", node, short(p.reportEvery))
}

// short schreibt eine Zeitspanne so knapp wie möglich: 168h statt 168h0m0s.
func short(d time.Duration) string {
	text := d.String()
	if rest, found := strings.CutSuffix(text, "h0m0s"); found {
		return rest + "h"
	}
	if rest, found := strings.CutSuffix(text, "m0s"); found {
		return rest + "m"
	}
	return text
}

func trimFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// validate lädt die erzeugte Datei einmal probeweise. Eine Konfiguration,
// die der Dienst nicht versteht, wäre ein schlechteres Ergebnis als gar
// keine — und der Fehler fiele erst beim nächsten Start auf.
func validate(content string) error {
	dir, err := os.MkdirTemp("", "pve-optimizer-wizard")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return err
	}
	_, err = config.Load(path)
	return err
}
