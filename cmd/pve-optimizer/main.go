// pve-optimizer beobachtet Proxmox und ergänzt nach jeder neu angelegten
// oder wiederhergestellten VM die fehlenden IO-Begrenzungen ihrer Platten.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"pve-optimizer/internal/config"
	"pve-optimizer/internal/proxmox"
	"pve-optimizer/internal/version"
	"pve-optimizer/internal/watcher"
)

func main() {
	configPath := flag.String("config", "/etc/pve-optimizer/config.yaml", "Pfad zur Konfigurationsdatei")
	showVersion := flag.Bool("version", false, "Version ausgeben und beenden")
	debug := flag.Bool("debug", false, "ausführliche Protokollierung")
	sweep := flag.Bool("sweep", false, "einmalig alle vorhandenen VMs anpassen und beenden")
	flag.Parse()

	if *showVersion {
		fmt.Printf("pve-optimizer %s (Build %s, %s)\n", version.Version, version.Build, version.BuiltAt)
		return
	}

	if err := run(*configPath, *debug, *sweep); err != nil {
		slog.Error("dienst beendet", "fehler", err)
		os.Exit(1)
	}
}

func run(configPath string, debug, sweep bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	log := newLogger(debug)
	client, err := newClient(cfg)
	if err != nil {
		return err
	}

	w, err := watcher.New(cfg, client, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("pve-optimizer gestartet", "version", version.Version, "build", version.Build)
	if sweep {
		return w.Sweep(ctx)
	}
	return w.Run(ctx)
}

func newClient(cfg *config.Config) (proxmox.Client, error) {
	switch cfg.Mode {
	case config.ModeAPI:
		return proxmox.NewAPIClient(
			cfg.API.URL, cfg.API.TokenID, cfg.API.TokenSecret, cfg.API.InsecureSkipVerify), nil
	case config.ModeLocal:
		return proxmox.NewLocalClient(), nil
	default:
		return nil, fmt.Errorf("unbekannter modus %q", cfg.Mode)
	}
}

func newLogger(debug bool) *slog.Logger {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
