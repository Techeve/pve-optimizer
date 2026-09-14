// Package config lädt die Konfiguration des Dienstes und löst auf, welches
// Drosselungsprofil für einen Speicherpool gilt.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"pve-optimizer/internal/limits"
	"pve-optimizer/internal/rules"
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
	Mode         Mode          `yaml:"mode"`
	PollInterval time.Duration `yaml:"poll_interval"`
	DryRun       bool          `yaml:"dry_run"`
	StateFile    string        `yaml:"state_file"`

	// OnlyOwnNode beschränkt den Dienst auf die VMs des Nodes, auf dem er
	// läuft. Nötig, wenn er auf jedem Node läuft: Auch im local-Modus
	// greift pvesh clusterweit zu, ohne diese Grenze würden sich mehrere
	// Instanzen dieselben VMs vornehmen.
	OnlyOwnNode *bool `yaml:"only_own_node"`
	// Node überschreibt den eigenen Node-Namen. Leer bedeutet: der
	// Hostname, unter dem der Node im Cluster geführt wird.
	Node string `yaml:"node"`

	API      API                       `yaml:"api"`
	Defaults limits.Profile            `yaml:"defaults"`
	Pools    map[string]limits.Profile `yaml:"pools"`

	// Rules sind die Regeln, die clusterweit gelten. Ausgewertet werden
	// sie im Paket rules — jede Regel kennt ihre eigenen Optionen.
	Rules map[string]yaml.Node `yaml:"rules"`
	// Nodes weicht davon ab, je Node. Genannt wird nur, was anders ist.
	Nodes map[string]NodeSettings `yaml:"nodes"`
}

// Node ist die Abweichung eines einzelnen Nodes. Alles, was hier fehlt,
// gilt so, wie es clusterweit eingestellt ist.
type NodeSettings struct {
	Rules    map[string]yaml.Node      `yaml:"rules"`
	Defaults limits.Profile            `yaml:"defaults"`
	Pools    map[string]limits.Profile `yaml:"pools"`
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

	if err := applyDefaults(&cfg); err != nil {
		return nil, err
	}

	if secret := os.Getenv(tokenSecretEnv); secret != "" {
		cfg.API.TokenSecret = secret
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyDefaults(cfg *Config) error {
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

	// Wer lokal läuft, läuft typischerweise auf jedem Node — dann muss sich
	// jede Instanz auf ihren eigenen beschränken. Über die Cluster-API
	// genügt dagegen eine Installation für alle.
	if cfg.OnlyOwnNode == nil {
		restrict := cfg.Mode == ModeLocal
		cfg.OnlyOwnNode = &restrict
	}

	if *cfg.OnlyOwnNode && cfg.Node == "" {
		hostname, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("eigenen node-namen ermitteln: %w", err)
		}
		cfg.Node = hostname
	}
	return nil
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
		if strings.Contains(name, ":") {
			return fmt.Errorf("pools.%s: profile je node stehen unter nodes.<node>.pools.<pool>", name)
		}
		if err := validateProfile("pools."+name, profile); err != nil {
			return err
		}
	}
	if err := c.validateNodes(); err != nil {
		return err
	}
	if err := c.validateRules(); err != nil {
		return err
	}
	if c.Mode == ModeAPI {
		return c.validateAPI()
	}
	return nil
}

func (c *Config) validateNodes() error {
	for node, settings := range c.Nodes {
		context := "nodes." + node
		if len(settings.Defaults) > 0 {
			if err := validateProfile(context+".defaults", settings.Defaults); err != nil {
				return err
			}
		}
		for name, profile := range settings.Pools {
			if err := validateProfile(context+".pools."+name, profile); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateRules baut die Regeln einmal für jeden genannten Node durch.
// Ein Tippfehler in einem Regelnamen oder einer Option soll den Dienst
// beim Start abbrechen lassen und nicht erst dann auffallen, wenn die
// gemeinte Regel monatelang stillschweigend aus war.
func (c *Config) validateRules() error {
	if _, err := c.RulesFor(""); err != nil {
		return err
	}
	for node := range c.Nodes {
		if _, err := c.RulesFor(node); err != nil {
			return fmt.Errorf("nodes.%s: %w", node, err)
		}
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
	if err := limits.CheckProfile(profile); err != nil {
		return fmt.Errorf("%s: %w", context, err)
	}
	return nil
}

// ProfileFor liefert das Profil für einen Speicherpool auf einem Node.
// Gesucht wird von speziell nach allgemein:
//
//	nodes.<node>.pools.<pool>   dieser Pool auf diesem Node
//	pools.<pool>                dieser Pool auf allen Nodes
//	nodes.<node>.defaults       alles Übrige auf diesem Node
//	defaults                    alles Übrige
//
// Die erste Stufe ist nötig, weil gleichnamige Pools auf verschiedenen
// Nodes auf völlig unterschiedlicher Hardware liegen können. Der zweite
// Rückgabewert nennt die Fundstelle — nützlich fürs Protokoll.
func (c *Config) ProfileFor(node, storage string) (limits.Profile, string) {
	settings := c.Nodes[node]

	if profile, found := settings.Pools[storage]; found {
		return profile, "nodes." + node + ".pools." + storage
	}
	if profile, found := c.Pools[storage]; found {
		return profile, "pools." + storage
	}
	if len(settings.Defaults) > 0 {
		return settings.Defaults, "nodes." + node + ".defaults"
	}
	return c.Defaults, "defaults"
}

// RulesFor baut die Regeln für einen Node: die clusterweiten Einstellungen,
// darüber die Abweichung dieses Nodes.
//
// Ein Probelauf wirkt hier und nicht in den Regeln selbst: dry_run senkt
// jede scharfe Regel auf "report" ab, protokolliert wird also alles,
// geschrieben nichts.
func (c *Config) RulesFor(node string) (rules.Set, error) {
	set, err := rules.Build(c.Rules, c.Nodes[node].Rules)
	if err != nil {
		return nil, err
	}
	if c.DryRun {
		return set.ReportOnly(), nil
	}
	return set, nil
}

// RestrictedToOwnNode meldet, ob sich der Dienst auf den eigenen Node
// beschränkt.
func (c *Config) RestrictedToOwnNode() bool {
	return c.OnlyOwnNode != nil && *c.OnlyOwnNode
}
