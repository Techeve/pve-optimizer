package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// state hält je Unit fest, wie oft der Monitor sie schon hochgeholt hat.
// Der Stand liegt auf der Platte, damit ein Update des Dienstes den Zähler
// nicht heimlich auf null setzt und die Grenze damit aushebelt.
type state struct {
	path  string
	Units map[string]*unitState `json:"units"`
}

type unitState struct {
	Restarts int `json:"restarts"`
	// Notified verhindert, dass dieselbe Störung immer wieder gemeldet
	// wird.
	Notified bool `json:"notified"`
}

func loadState(path string) (*state, error) {
	loaded := &state{path: path, Units: map[string]*unitState{}}

	data, err := os.ReadFile(path) //nolint:gosec // der Pfad kommt aus der Konfiguration
	if errors.Is(err, fs.ErrNotExist) {
		return loaded, nil
	}
	if err != nil {
		return nil, fmt.Errorf("zählerstand lesen: %w", err)
	}
	if err := json.Unmarshal(data, loaded); err != nil {
		return nil, fmt.Errorf("zählerstand auswerten: %w", err)
	}
	if loaded.Units == nil {
		loaded.Units = map[string]*unitState{}
	}
	return loaded, nil
}

func (s *state) unit(name string) *unitState {
	if found, ok := s.Units[name]; ok {
		return found
	}
	fresh := &unitState{}
	s.Units[name] = fresh
	return fresh
}

func (s *state) restarts(name string) int  { return s.unit(name).Restarts }
func (s *state) notified(name string) bool { return s.unit(name).Notified }
func (s *state) count(name string)         { s.unit(name).Restarts++ }
func (s *state) markNotified(name string)  { s.unit(name).Notified = true }

func (s *state) reset(name string) {
	s.Units[name] = &unitState{}
}

// save schreibt über eine temporäre Datei, damit ein Absturz mitten im
// Schreiben keinen halben Stand hinterlässt.
func (s *state) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("verzeichnis für den zählerstand anlegen: %w", err)
	}

	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("zählerstand kodieren: %w", err)
	}

	temp := s.path + ".tmp"
	if err := os.WriteFile(temp, data, 0o644); err != nil { //nolint:gosec // der Stand ist kein Geheimnis
		return fmt.Errorf("zählerstand schreiben: %w", err)
	}
	if err := os.Rename(temp, s.path); err != nil {
		return fmt.Errorf("zählerstand übernehmen: %w", err)
	}
	return nil
}
