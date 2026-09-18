// Package services überwacht systemd-Dienste des Nodes und startet sie
// neu, wenn systemd sie als abgestürzt führt.
//
// Hintergrund ist eine Eigenheit von Proxmox: Units wie pvestatd.service
// bringen kein "Restart=" mit. Stirbt so ein Dienst an einem Signal,
// bleibt er liegen, bis jemand ihn von Hand startet — auf vmh03 waren das
// am 17.09.2026 gut siebzehn Stunden ohne Statuswerte.
//
// Blind endlos neu starten hilft aber auch nicht: Ein Dienst, der immer
// wieder stirbt, hat eine Ursache, die ein Neustart nicht behebt. Deshalb
// zählt der Monitor mit, steigt nach einer einstellbaren Zahl von
// Versuchen aus und meldet sich dann bei einem Menschen.
package services

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"pve-optimizer/internal/mail"
	"pve-optimizer/internal/mode"
)

// Settings ist die Konfiguration des Monitors.
type Settings struct {
	// Mode bestimmt, wie weit der Monitor geht. "report" protokolliert
	// den Absturz nur — er startet dann nichts neu und verschickt auch
	// keine Mail.
	Mode mode.Mode `yaml:"mode"`
	// Units sind die zu überwachenden systemd-Units.
	Units []string `yaml:"units"`
	// RestartLimit ist die Zahl der Neustarts, die der Monitor je Unit
	// unternimmt, bevor er aufgibt und meldet.
	RestartLimit int `yaml:"restart_limit"`
	// StableAfter ist die Laufzeit, nach der ein Dienst wieder als gesund
	// gilt: Der Zähler fällt dann auf null, und die nächste Störung
	// beginnt von vorn.
	StableAfter time.Duration `yaml:"stable_after"`
}

// DefaultSettings sind die Vorgaben. Aus wie alles, was eingreift —
// einschalten muss ihn, wer ihn haben will.
func DefaultSettings() Settings {
	return Settings{
		Mode:         mode.Off,
		Units:        []string{"pvestatd.service"},
		RestartLimit: 3,
		StableAfter:  24 * time.Hour,
	}
}

func (s Settings) Check() error {
	if s.Mode == mode.Off {
		return nil
	}
	if len(s.Units) == 0 {
		return fmt.Errorf("units fehlt: ohne dienst hätte der monitor nichts zu tun")
	}
	for _, unit := range s.Units {
		if unit == "" {
			return fmt.Errorf("units enthält einen leeren eintrag")
		}
	}
	if s.RestartLimit < 1 {
		return fmt.Errorf("restart_limit muss mindestens 1 sein, ist %d", s.RestartLimit)
	}
	// Eine kurze Frist würde den Zähler zwischen zwei Abstürzen zurücksetzen
	// und das Limit damit wirkungslos machen.
	if s.StableAfter < time.Hour {
		return fmt.Errorf("stable_after muss mindestens 1h sein, ist %s", s.StableAfter)
	}
	return nil
}

// Monitor überwacht die eingestellten Units.
type Monitor struct {
	settings Settings
	node     string
	systemd  systemd
	mail     mail.Sender
	state    *state
	log      *slog.Logger
	now      func() time.Time
}

// New baut den Monitor. statePath ist die Datei, in der die Zähler einen
// Neustart des Dienstes überdauern; sender ist der Weg, auf dem die
// Meldung hinausgeht — er gehört dem ganzen Dienst und wird hier nur
// benutzt.
func New(settings Settings, node, statePath string, sender mail.Sender, log *slog.Logger) (*Monitor, error) {
	loaded, err := loadState(statePath)
	if err != nil {
		return nil, err
	}
	return &Monitor{
		settings: settings,
		node:     node,
		systemd:  systemctl{},
		mail:     sender,
		state:    loaded,
		log:      log,
		now:      time.Now,
	}, nil
}

// Run prüft bis zum Abbruch des Kontexts.
func (m *Monitor) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Der Node steht mit im Protokoll, weil der Monitor immer nur die
	// Maschine sieht, auf der er läuft — im api-Modus ist das nicht
	// zwangsläufig der Node, dessen Gäste der Dienst betreut.
	m.log.Info("dienst-monitor gestartet", "node", m.node,
		"modus", m.settings.Mode, "units", m.settings.Units,
		"neustarts", m.settings.RestartLimit, "stabil ab", m.settings.StableAfter,
		"meldung", m.mail.Describe())

	for {
		m.CheckOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// CheckOnce sieht einmal nach jeder Unit. Fehler bei einer Unit dürfen die
// übrigen nicht aufhalten, deshalb werden sie hier protokolliert statt
// zurückgegeben.
func (m *Monitor) CheckOnce(ctx context.Context) {
	var changed bool
	for _, unit := range m.settings.Units {
		touched, err := m.check(ctx, unit)
		if err != nil {
			m.log.Error("dienst nicht geprüft", "unit", unit, "fehler", err)
		}
		changed = changed || touched
	}

	if changed {
		if err := m.state.save(); err != nil {
			m.log.Error("zählerstand nicht gesichert", "fehler", err)
		}
	}
}

// check sieht nach einer Unit und meldet, ob sich der Zählerstand geändert
// hat.
func (m *Monitor) check(ctx context.Context, unit string) (bool, error) {
	current, err := m.systemd.Status(ctx, unit)
	if err != nil {
		return false, err
	}
	if !current.Known {
		return false, fmt.Errorf("systemd kennt die unit nicht")
	}
	if current.Active {
		return m.noteStable(unit, current.Since), nil
	}
	if !current.Failed {
		// Angehalten, aber nicht fehlgeschlagen: Das hat jemand so
		// gewollt. Wer einen Dienst abschaltet, will ihn nicht von einem
		// Wächter wieder hochgeholt bekommen.
		return false, nil
	}
	return m.handleFailure(ctx, unit)
}

// noteStable setzt den Zähler zurück, sobald der Dienst lange genug
// durchgelaufen ist.
func (m *Monitor) noteStable(unit string, since time.Time) bool {
	count := m.state.restarts(unit)
	if count == 0 {
		return false
	}
	uptime := m.now().Sub(since)
	if uptime < m.settings.StableAfter {
		return false
	}

	m.log.Info("dienst wieder stabil, zähler zurückgesetzt",
		"unit", unit, "laufzeit", uptime.Truncate(time.Minute), "vorher", count)
	m.state.reset(unit)
	return true
}

func (m *Monitor) handleFailure(ctx context.Context, unit string) (bool, error) {
	log := m.log.With("unit", unit)
	count := m.state.restarts(unit)

	if m.settings.Mode == mode.Report {
		log.Info("dienst ist abgestürzt, würde neu gestartet", "bisherige neustarts", count)
		return false, nil
	}

	if count >= m.settings.RestartLimit {
		return m.giveUp(unit, count), nil
	}

	log.Warn("dienst ist abgestürzt, wird neu gestartet",
		"versuch", count+1, "von", m.settings.RestartLimit)
	if err := m.systemd.Restart(ctx, unit); err != nil {
		// Auch ein gescheiterter Versuch ist verbraucht. Sonst liefe der
		// Monitor bei einem Dienst, der sich gar nicht mehr starten lässt,
		// endlos im Kreis, ohne je zu melden.
		m.state.count(unit)
		return true, err
	}
	m.state.count(unit)
	return true, nil
}

// giveUp meldet einmal und hält sich danach zurück. Ohne diese Sperre
// schriebe der Monitor bei jedem Durchlauf eine weitere Mail — bei einem
// Dienst, der dauerhaft am Boden liegt, alle dreißig Sekunden eine.
func (m *Monitor) giveUp(unit string, count int) bool {
	log := m.log.With("unit", unit, "neustarts", count)
	if m.state.notified(unit) {
		log.Debug("dienst weiterhin abgestürzt, bereits gemeldet")
		return false
	}

	log.Error("dienst bleibt abgestürzt, keine neustarts mehr", "grenze", m.settings.RestartLimit)
	subject, body := m.message(unit, count)
	if err := m.mail.Send(subject, body); err != nil {
		// Nicht als gemeldet vermerken: Beim nächsten Durchlauf soll es
		// erneut versucht werden.
		log.Error("meldung nicht verschickt", "fehler", err)
		return false
	}

	m.state.markNotified(unit)
	return true
}

func (m *Monitor) message(unit string, count int) (subject, body string) {
	subject = fmt.Sprintf("%s: %s ist %d-mal abgestürzt", m.node, unit, count)
	body = fmt.Sprintf(`Der Dienst %s auf dem Node %s ist abgestürzt und wurde
bereits %d-mal neu gestartet. Damit ist die eingestellte Grenze (%d)
erreicht — pve-optimizer startet ihn nicht mehr von selbst.

Ein Dienst, der so oft stirbt, hat eine Ursache, die ein Neustart nicht
behebt. Ein Blick ins Journal des Nodes ist jetzt fällig:

    journalctl -u %s --since "-1 day"

Der Zähler fällt von selbst auf null zurück, sobald der Dienst wieder
%s am Stück durchgelaufen ist.

-- 
pve-optimizer auf %s, %s
`, unit, m.node, count, m.settings.RestartLimit, unit,
		kurz(m.settings.StableAfter), m.node, m.now().Format(time.RFC1123Z))
	return subject, body
}

// kurz schreibt eine Zeitspanne so, wie sie in der Konfiguration steht.
// Go gibt sie sonst bis zur letzten Sekunde aus — "24h0m0s" in einer Mail,
// die jemand nachts um drei liest, ist eine Zumutung.
func kurz(d time.Duration) string {
	text := d.String()
	if rest, found := strings.CutSuffix(text, "h0m0s"); found {
		return rest + "h"
	}
	if rest, found := strings.CutSuffix(text, "m0s"); found {
		return rest + "m"
	}
	return text
}
