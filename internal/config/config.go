// Package config lädt die Konfiguration des Dienstes und löst auf, welches
// Drosselungsprofil für einen Speicherpool gilt.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"pve-optimizer/internal/limits"
)

// Mode bestimmt, wie der Dienst mit Proxmox spricht.
type Mode string

const (
	// ModeAPI spricht über HTTPS mit der Cluster-API und sieht damit alle
	// Nodes. Braucht einen API-Token.
	ModeAPI Mode = "api"
	// ModeLocal ruft pvesh auf dem Node auf, auf dem der Dienst läuft.
	// Braucht keinen Token, muss aber auf jedem Node laufen.
	ModeLocal Mode = "local"
)

// Umgebungsvariable für das Token-Secret. Secrets gehören nicht in eine
// Konfigurationsdatei, die neben dem Binary liegt.
const tokenSecretEnv = "PVE_OPTIMIZER_TOKEN_SECRET"

type Config struct {
	Mode         Mode                      `yaml:"mode"`
	PollInterval time.Duration             `yaml:"poll_interval"`
	DryRun       bool                      `yaml:"dry_run"`
	StateFile    string                    `yaml:"state_file"`
	API          API                       `yaml:"api"`
	Defaults     limits.Profile            `yaml:"defaults"`
	Pools        map[string]limits.Profile `yaml:"pools"`
}

type API struct {
	URL                string `yaml:"url"`
	TokenID            string `yaml:"token_id"`
	TokenSecret        string `yaml:"token_secret"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

// Load liest die Konfigurationsdatei, füllt Vorgabewerte und prüft sie.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("konfiguration lesen: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("konfiguration auswerten: %w", err)
	}

	applyDefaults(&cfg)

	if secret := os.Getenv(tokenSecretEnv); secret != "" {
		cfg.API.TokenSecret = secret
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Mode == "" {
		cfg.Mode = ModeLocal
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.StateFile == "" {
		cfg.StateFile = "/var/lib/pve-optimizer/state.json"
	}
	if cfg.Pools == nil {
		cfg.Pools = map[string]limits.Profile{}
	}
}

func (c *Config) validate() error {
	if c.Mode != ModeAPI && c.Mode != ModeLocal {
		return fmt.Errorf("mode muss %q oder %q sein, ist %q", ModeAPI, ModeLocal, c.Mode)
	}
	if c.PollInterval < time.Second {
		return fmt.Errorf("poll_interval muss mindestens 1s sein, ist %s", c.PollInterval)
	}
	if len(c.Defaults) == 0 {
		return fmt.Errorf("defaults fehlt: ohne Standardprofil bliebe ein unbekannter pool ungedrosselt")
	}
	if err := validateProfile("defaults", c.Defaults); err != nil {
		return err
	}
	for name, profile := range c.Pools {
		if err := validateProfile("pools."+name, profile); err != nil {
			return err
		}
	}
	if c.Mode == ModeAPI {
		return c.validateAPI()
	}
	return nil
}

func (c *Config) validateAPI() error {
	switch {
	case c.API.URL == "":
		return fmt.Errorf("api.url fehlt, wird für mode %q gebraucht", ModeAPI)
	case c.API.TokenID == "":
		return fmt.Errorf("api.token_id fehlt, wird für mode %q gebraucht", ModeAPI)
	case c.API.TokenSecret == "":
		return fmt.Errorf("api-token-secret fehlt: in der konfiguration oder über %s setzen", tokenSecretEnv)
	}
	return nil
}

// validateProfile weist unbekannte Schlüssel ab. Ein Tippfehler wie
// "mpbs_rd" würde sonst stillschweigend ignoriert und die Platte bliebe
// ungedrosselt.
func validateProfile(context string, profile limits.Profile) error {
	known := map[string]bool{}
	for _, key := range limits.Keys {
		known[key] = true
	}
	for key, value := range profile {
		if !known[key] {
			return fmt.Errorf("%s: unbekannter schlüssel %q", context, key)
		}
		if value < 0 {
			return fmt.Errorf("%s.%s: wert darf nicht negativ sein", context, key)
		}
	}
	return nil
}

// ProfileFor liefert das Profil des Speicherpools und, falls keines
// hinterlegt ist, die Vorgabe. Der zweite Rückgabewert sagt, ob ein eigenes
// Profil gefunden wurde — nützlich fürs Protokoll.
func (c *Config) ProfileFor(storage string) (limits.Profile, bool) {
	if profile, found := c.Pools[storage]; found {
		return profile, true
	}
	return c.Defaults, false
}
