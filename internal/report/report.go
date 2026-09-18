// Package report verschickt in festem Abstand, was die Empfehlungen
// gefunden haben.
//
// Der Anlass ist eine Lücke, die sich von selbst auftut: Ein
// Sicherungsauftrag mit fester VMID-Liste erfasst neue Gäste nicht. Wer
// eine VM anlegt, hat sie damit nicht gesichert — und merkt es erst, wenn
// er sie zurückholen will. Ein Bericht, der alle paar Tage nachsieht,
// findet das, bevor es zählt.
//
// Verschickt wird nur, wenn es etwas zu melden gibt. Eine Mail, die jede
// Woche "alles in Ordnung" sagt, liest nach dem vierten Mal niemand mehr —
// und dann auch die nicht, in der etwas steht.
package report

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"pve-optimizer/internal/advice"
	"pve-optimizer/internal/mail"
	"pve-optimizer/internal/mode"
)

// Settings ist die Konfiguration des Berichts.
type Settings struct {
	// Mode bestimmt, wie weit der Bericht geht. "report" schreibt die
	// Befunde nur ins Protokoll und verschickt nichts.
	Mode mode.Mode `yaml:"mode"`
	// Node ist der Node, der verschickt. Ohne ihn würden bei einer
	// Installation je Node drei gleichlautende Mails hinausgehen.
	Node string `yaml:"node"`
	// Every ist der Abstand zwischen zwei Berichten.
	Every time.Duration `yaml:"every"`
}

func DefaultSettings() Settings {
	return Settings{Mode: mode.Off, Every: 7 * 24 * time.Hour}
}

func (s Settings) Check() error {
	if s.Mode == mode.Off {
		return nil
	}
	if s.Node == "" {
		return errors.New(
			"node fehlt: ohne ihn verschickt jede instanz denselben bericht —" +
				" bei einer installation je node also einmal pro node")
	}
	if s.Every < time.Hour {
		return fmt.Errorf("every muss mindestens 1h sein, ist %s", s.Every)
	}
	return nil
}

// Reporter sieht in festem Abstand nach und meldet, was aufgefallen ist.
type Reporter struct {
	settings Settings
	node     string
	checks   advice.Set
	cluster  advice.Cluster
	mail     mail.Sender
	state    *state
	log      *slog.Logger
	now      func() time.Time
}

// New baut den Bericht. statePath ist die Datei, in der der Zeitpunkt des
// letzten Versands einen Neustart des Dienstes überdauert — sonst begänne
// der Rhythmus bei jedem Update von vorn.
func New(
	settings Settings, node, statePath string,
	checks advice.Set, cluster advice.Cluster, sender mail.Sender, log *slog.Logger,
) (*Reporter, error) {
	loaded, err := loadState(statePath)
	if err != nil {
		return nil, err
	}
	return &Reporter{
		settings: settings, node: node, checks: checks, cluster: cluster,
		mail: sender, state: loaded, log: log, now: time.Now,
	}, nil
}

// Responsible meldet, ob dieser Node den Bericht verschickt.
func (r *Reporter) Responsible() bool {
	return r.settings.Node == r.node
}

// Run prüft bis zum Abbruch des Kontexts, ob der nächste Bericht fällig ist.
func (r *Reporter) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	r.log.Info("bericht eingerichtet",
		"node", r.node, "modus", r.settings.Mode, "abstand", r.settings.Every,
		"prüfungen", r.checks.Modes(), "versand", r.mail.Describe())

	for {
		r.CheckOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// CheckOnce sieht nach, ob ein Bericht fällig ist, und erledigt ihn.
//
// Der erste Bericht geht gleich nach dem Einschalten hinaus. So zeigt sich
// sofort, ob der Mailweg steht — und nicht erst in einer Woche, wenn der
// erste Bericht ausbleibt und niemand weiß, ob das gut oder schlecht ist.
func (r *Reporter) CheckOnce(ctx context.Context) {
	if !r.due() {
		return
	}

	findings, problems := r.checks.Run(ctx, r.cluster)
	for _, problem := range problems {
		r.log.Error("prüfung im bericht fehlgeschlagen", "fehler", problem)
	}
	// Bei einem Aussetzer nicht als erledigt vermerken: Sonst fiele der
	// Bericht dieser Woche aus, weil die API zwei Minuten nicht da war.
	if len(problems) > 0 {
		return
	}

	if len(findings) == 0 {
		r.log.Info("bericht fällig, nichts zu melden")
		r.markDone()
		return
	}
	r.send(findings)
}

func (r *Reporter) due() bool {
	last := r.state.lastSent()
	if last.IsZero() {
		return true
	}
	return r.now().Sub(last) >= r.settings.Every
}

func (r *Reporter) send(findings []advice.Finding) {
	log := r.log.With("befunde", len(findings))

	if r.settings.Mode == mode.Report {
		for _, finding := range findings {
			log.Info("im bericht", "prüfung", finding.Check, "betrifft", finding.Subject)
		}
		r.markDone()
		return
	}

	subject, body := r.message(findings)
	if err := r.mail.Send(subject, body); err != nil {
		// Nicht als erledigt vermerken — beim nächsten Durchlauf erneut
		// versuchen.
		log.Error("bericht nicht verschickt", "fehler", err)
		return
	}
	log.Info("bericht verschickt", "an", r.mail.Describe())
	r.markDone()
}

func (r *Reporter) markDone() {
	r.state.setLastSent(r.now())
	if err := r.state.save(); err != nil {
		r.log.Error("zeitpunkt des berichts nicht gesichert", "fehler", err)
	}
}

func (r *Reporter) message(findings []advice.Finding) (subject, body string) {
	subject = fmt.Sprintf("%s: pve-optimizer meldet %d Befund(e)", r.node, len(findings))

	var text strings.Builder
	fmt.Fprintf(&text, "Der regelmäßige Blick auf den Cluster hat %d Punkt(e) gefunden.\n", len(findings))
	text.WriteString("Geändert wurde nichts — das hier sind Empfehlungen.\n")

	for _, group := range advice.Group(findings) {
		fmt.Fprintf(&text, "\n%s\n%s\n\n", group.Check, strings.Repeat("-", len(group.Check)))
		for _, subject := range group.Subjects {
			fmt.Fprintf(&text, "  - %s\n", subject)
		}
		fmt.Fprintf(&text, "\n  Warum:   %s\n  Was tun: %s\n", group.Why, group.Action)
	}

	fmt.Fprintf(&text, `
Nachsehen lässt sich das jederzeit auf dem Node:

    pve-optimizer -check

Wer einen dieser Punkte bewusst so lassen will, schaltet die zugehörige
Prüfung unter "advice:" ab — dann steht sie auch hier nicht mehr.

-- 
pve-optimizer auf %s, %s
`, r.node, r.now().Format(time.RFC1123Z))

	return subject, text.String()
}
