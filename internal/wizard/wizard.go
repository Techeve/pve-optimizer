// Package wizard führt durch die Ersteinrichtung.
//
// Der Wizard fragt nicht ins Blaue: Er sieht sich erst den Cluster an und
// schlägt vor, was dort tatsächlich steht — die vorhandenen Speicher, den
// eigenen Node, die Gäste, die kein Sicherungsauftrag erfasst. Jede Frage
// hat eine Vorgabe, die eine leere Eingabe übernimmt.
//
// Geschrieben wird erst, wenn die erzeugte Konfiguration sich auch laden
// lässt: Eine Datei, die der Dienst nicht versteht, wäre ein schlechteres
// Ergebnis als gar keine.
package wizard

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pve-optimizer/internal/mode"
	"pve-optimizer/internal/proxmox"
)

// Wizard führt das Gespräch.
type Wizard struct {
	prompt *prompt
	client proxmox.Client
	node   string
	now    func() time.Time
}

func New(in io.Reader, out io.Writer, client proxmox.Client, node string) *Wizard {
	return &Wizard{prompt: newPrompt(in, out), client: client, node: node, now: time.Now}
}

// umgebung ist, was der Wizard am Cluster vorfindet.
type umgebung struct {
	pools  []string
	offen  []proxmox.Guest
	gaeste int
}

// Run führt durch die Einrichtung und schreibt die Datei.
func (w *Wizard) Run(ctx context.Context, configPath string) error {
	p := w.prompt
	p.say("pve-optimizer — Einrichtung")
	p.say("Enter übernimmt jeweils den Wert in eckigen Klammern.\n")

	found := w.inspect(ctx)
	plan := w.askAll(found)

	yaml := w.render(plan, found)
	p.say("\n--- erzeugte Konfiguration ---\n%s--- Ende ---\n", yaml)

	if err := validate(yaml); err != nil {
		return fmt.Errorf("die erzeugte konfiguration ist nicht ladbar: %w", err)
	}
	p.say("Die Konfiguration wurde geprüft und ist ladbar.")

	if !p.yesNo(fmt.Sprintf("Nach %s schreiben?", configPath), true) {
		p.say("Nichts geschrieben.")
		return nil
	}
	return w.write(configPath, yaml)
}

// inspect sieht sich den Cluster an. Fehler sind hier kein Abbruch: Der
// Wizard soll auch dann durchlaufen, wenn die API klemmt — dann eben ohne
// Vorschläge.
func (w *Wizard) inspect(ctx context.Context) umgebung {
	var found umgebung

	if storages, err := w.client.Storages(ctx); err != nil {
		w.prompt.say("Hinweis: Speicher nicht abrufbar (%v) — keine Vorschläge dazu.", err)
	} else {
		for _, storage := range storages {
			if storage.HoldsGuests() {
				found.pools = append(found.pools, storage.Name)
			}
		}
		sort.Strings(found.pools)
	}

	if guests, err := w.client.ListGuests(ctx); err == nil {
		found.gaeste = len(guests)
	}
	if offen, err := w.client.NotBackedUp(ctx); err != nil {
		w.prompt.say("Hinweis: ungesicherte Gäste nicht abrufbar (%v).", err)
	} else {
		found.offen = offen
	}

	w.prompt.say("Gefunden auf %s:", w.node)
	w.prompt.say("  Speicher mit Gastplatten: %s", oderKeine(strings.Join(found.pools, ", ")))
	w.prompt.say("  Gäste im Cluster:         %d", found.gaeste)
	w.prompt.say("  davon ohne Sicherung:     %d\n", len(found.offen))
	return found
}

// plan ist, was der Wizard erfragt hat.
type plan struct {
	mbpsRd, mbpsWr int
	rules          map[string]mode.Mode
	startupVM      time.Duration
	startupLXC     time.Duration
	netRate        float64
	monitor        bool
	monitorLimit   int
	mailTo         string
	mailServer     string
	report         bool
	reportEvery    time.Duration
	ignore         []int
}

func (w *Wizard) askAll(found umgebung) plan {
	p := w.prompt
	result := plan{rules: map[string]mode.Mode{}}

	p.say("── Drosselung der Platten ──")
	p.say("Gilt für jeden Speicher, der später keine eigenen Werte bekommt.")
	result.mbpsRd = p.number("Dauerrate lesen (MB/s)", 200)
	result.mbpsWr = p.number("Dauerrate schreiben (MB/s)", 150)
	if len(found.pools) > 0 {
		p.say("  Profile je Speicher trägst du später nach — config.example.yaml zeigt wie.\n")
	}

	p.say("\n── Regeln ──")
	p.say("\"report\" schreibt nur ins Protokoll, was die Regel täte. Ein guter Anfang.")
	result.rules["io_limits"] = p.mode("IO-Begrenzung aus den Profilen", mode.Enforce)
	result.rules["discard"] = p.mode("Gelöschte Blöcke zurückgeben (discard)", mode.Report)
	result.rules["ssd"] = p.mode("Platte als SSD kennzeichnen", mode.Report)
	result.rules["iothread"] = p.mode("Eigener Thread je Platte (iothread)", mode.Report)
	result.rules["guest_agent"] = p.mode("QEMU-Gast-Agenten erlauben", mode.Report)

	result.rules["startup"] = p.mode("Start nach Neustart staffeln", mode.Report)
	if result.rules["startup"] != mode.Off {
		result.startupVM = p.duration("  Abstand nach einer VM", 45*time.Second)
		result.startupLXC = p.duration("  Abstand nach einem Container", 10*time.Second)
	}

	result.rules["net_rate"] = p.mode("Durchsatz je Netzwerkkarte begrenzen", mode.Off)
	if result.rules["net_rate"] != mode.Off {
		p.say("  Die Einheit ist MB/s, nicht MBit/s — eine Gigabit-Leitung sind 125.")
		result.netRate = float64(p.number("  Rate (MB/s)", 125))
	}

	p.say("\n── Dienst-Monitor ──")
	p.say("Proxmox-Units haben kein \"Restart=\". Stirbt pvestatd, bleibt er liegen.")
	result.monitor = p.yesNo("Abgestürzte Dienste wieder hochholen?", true)
	if result.monitor {
		result.monitorLimit = p.number("  Neustarts je Dienst, dann ist Schluss", 3)
	}

	p.say("\n── Mail ──")
	p.say("Ohne Mailserver bleiben alle Meldungen im Protokoll — das ist in Ordnung.")
	result.mailServer = p.ask("Mailserver als host:port (leer = keiner)", "")
	if result.mailServer != "" {
		result.mailTo = p.ask("  Empfänger", "")
	}

	p.say("\n── Regelmäßiger Bericht ──")
	p.say("Meldet Gäste ohne Sicherung und weitere ungünstige Einstellungen.")
	result.report = p.yesNo("Bericht einschalten?", result.mailServer != "")
	if result.report {
		result.reportEvery = p.duration("  Abstand", 7*24*time.Hour)
	}

	result.ignore = w.askIgnore(found)
	return result
}

// askIgnore zeigt die Gäste, die kein Sicherungsauftrag erfasst, und fragt,
// welche davon bewusst keine brauchen. Ohne diese Liste meldet der Bericht
// jede Woche dieselben Wegwerf-Gäste — und wird nach vier Wochen
// weggeklickt.
func (w *Wizard) askIgnore(found umgebung) []int {
	if len(found.offen) == 0 {
		return nil
	}

	p := w.prompt
	p.say("\n── Gäste ohne Sicherung ──")
	for _, guest := range found.offen {
		art := "VM"
		if guest.Kind == proxmox.KindLXC {
			art = "Container"
		}
		p.say("  %-9s %-6d %s", art, guest.VMID, guest.Name)
	}
	p.say("Welche davon brauchen bewusst keine Sicherung? Die tauchen dann nicht mehr auf.")
	return p.vmids("Nummern (leer = alle sollen gemeldet werden)", nil)
}

func (w *Wizard) write(configPath, content string) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("verzeichnis anlegen: %w", err)
	}

	// Eine vorhandene Konfiguration wird gesichert, bevor sie ersetzt
	// wird. Wer den Wizard aus Neugier startet, soll nicht seine
	// gewachsene Datei verlieren.
	if _, err := os.Stat(configPath); err == nil {
		backup := configPath + ".vor-wizard"
		if err := copyFile(configPath, backup); err != nil {
			return fmt.Errorf("bisherige konfiguration sichern: %w", err)
		}
		w.prompt.say("Bisherige Konfiguration gesichert nach %s", backup)
	}

	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil { //nolint:gosec // die Konfiguration ist kein Geheimnis
		return fmt.Errorf("konfiguration schreiben: %w", err)
	}
	w.prompt.say("Geschrieben nach %s", configPath)
	return nil
}

func copyFile(from, to string) error {
	data, err := os.ReadFile(from) //nolint:gosec // der Pfad kommt vom Aufrufer
	if err != nil {
		return err
	}
	return os.WriteFile(to, data, 0o644) //nolint:gosec // wie das Original
}

func oderKeine(text string) string {
	if text == "" {
		return "(keine gefunden)"
	}
	return text
}
