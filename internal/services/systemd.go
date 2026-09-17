package services

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// status ist der Zustand einer Unit, soweit der Monitor ihn braucht.
type status struct {
	// Known meldet, ob systemd die Unit überhaupt kennt.
	Known bool
	// Failed meldet, ob systemd sie als fehlgeschlagen führt. Nur dieser
	// Zustand löst einen Neustart aus — ein von Hand angehaltener Dienst
	// steht auf "inactive" und soll angehalten bleiben.
	Failed bool
	// Active meldet, ob sie gerade läuft.
	Active bool
	// Since ist der Zeitpunkt, seit dem sie läuft. Nur bei Active belegt.
	Since time.Time
}

// systemd ist der Ausschnitt von systemd, den der Monitor benutzt. Als
// Schnittstelle, damit die Tests ohne einen echten Init auskommen.
type systemd interface {
	Status(ctx context.Context, unit string) (status, error)
	Restart(ctx context.Context, unit string) error
}

// systemctl spricht über das gleichnamige Kommando mit systemd. Der Umweg
// über das Kommando statt über den D-Bus spart eine Abhängigkeit; der
// Monitor fragt alle paar Sekunden eine Handvoll Units ab, der Aufwand
// fällt daneben nicht ins Gewicht.
type systemctl struct{}

func (systemctl) Status(ctx context.Context, unit string) (status, error) {
	// --timestamp=unix liefert "@1789684658" statt einer Datumszeile in
	// der Sprache des Systems.
	out, err := exec.CommandContext(ctx, "systemctl", "show", unit,
		"--timestamp=unix",
		"-p", "LoadState", "-p", "ActiveState", "-p", "ActiveEnterTimestamp",
	).Output()
	if err != nil {
		return status{}, fmt.Errorf("systemctl show %s: %w", unit, err)
	}
	return parseStatus(string(out))
}

func (systemctl) Restart(ctx context.Context, unit string) error {
	if out, err := exec.CommandContext(ctx, "systemctl", "restart", unit).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// parseStatus liest die Zeilen "Schlüssel=Wert", die systemctl show
// ausgibt. Eine unbekannte Unit ist kein Fehler des Kommandos — es
// antwortet dann mit LoadState=not-found.
func parseStatus(out string) (status, error) {
	fields := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if found {
			fields[key] = value
		}
	}

	state := fields["ActiveState"]
	result := status{
		Known:  fields["LoadState"] == "loaded",
		Failed: state == "failed",
		Active: state == "active",
	}
	if !result.Active {
		return result, nil
	}

	since, err := parseTimestamp(fields["ActiveEnterTimestamp"])
	if err != nil {
		return status{}, err
	}
	result.Since = since
	return result, nil
}

func parseTimestamp(value string) (time.Time, error) {
	seconds, err := strconv.ParseInt(strings.TrimPrefix(value, "@"), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("zeitstempel %q nicht lesbar: %w", value, err)
	}
	return time.Unix(seconds, 0), nil
}
