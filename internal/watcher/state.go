package watcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// State merkt sich, bis wohin die Aufgabenliste bereits abgearbeitet ist.
// Ohne diesen Stand würde der Dienst nach einem Neustart alle noch in der
// Historie stehenden Aufgaben erneut bearbeiten.
type State struct {
	LastEndTime int64 `json:"last_end_time"`
}

// LoadState liest den Stand. Fehlt die Datei, beginnt der Dienst bei der
// aktuellen Zeit — bereits erledigte Aufgaben aus der Vergangenheit sollen
// beim ersten Start nicht nachträglich abgearbeitet werden.
func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &State{LastEndTime: time.Now().Unix()}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stand lesen: %w", err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("stand auswerten: %w", err)
	}
	return &state, nil
}

// Save schreibt den Stand über eine temporäre Datei, damit ein Absturz
// mitten im Schreiben keine halbe Datei hinterlässt.
func (s *State) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("verzeichnis für den stand anlegen: %w", err)
	}

	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("stand kodieren: %w", err)
	}

	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o644); err != nil { //nolint:gosec // der Stand ist kein Geheimnis
		return fmt.Errorf("stand schreiben: %w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("stand übernehmen: %w", err)
	}
	return nil
}
