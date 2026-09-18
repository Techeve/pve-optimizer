package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// state merkt sich, wann der letzte Bericht hinausging. Auf der Platte,
// damit ein Update des Dienstes den Rhythmus nicht von vorn beginnen lässt
// — sonst käme nach jedem Paketwechsel eine Mail.
type state struct {
	path     string
	LastSent int64 `json:"last_sent"`
}

func loadState(path string) (*state, error) {
	loaded := &state{path: path}

	data, err := os.ReadFile(path) //nolint:gosec // der Pfad kommt aus der Konfiguration
	if errors.Is(err, fs.ErrNotExist) {
		return loaded, nil
	}
	if err != nil {
		return nil, fmt.Errorf("zeitpunkt des berichts lesen: %w", err)
	}
	if err := json.Unmarshal(data, loaded); err != nil {
		return nil, fmt.Errorf("zeitpunkt des berichts auswerten: %w", err)
	}
	return loaded, nil
}

func (s *state) lastSent() time.Time {
	if s.LastSent == 0 {
		return time.Time{}
	}
	return time.Unix(s.LastSent, 0)
}

func (s *state) setLastSent(when time.Time) { s.LastSent = when.Unix() }

// save schreibt über eine temporäre Datei, damit ein Absturz mitten im
// Schreiben keinen halben Stand hinterlässt.
func (s *state) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("verzeichnis für den bericht anlegen: %w", err)
	}

	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("zeitpunkt des berichts kodieren: %w", err)
	}

	temp := s.path + ".tmp"
	if err := os.WriteFile(temp, data, 0o644); err != nil { //nolint:gosec // der Stand ist kein Geheimnis
		return fmt.Errorf("zeitpunkt des berichts schreiben: %w", err)
	}
	if err := os.Rename(temp, s.path); err != nil {
		return fmt.Errorf("zeitpunkt des berichts übernehmen: %w", err)
	}
	return nil
}
