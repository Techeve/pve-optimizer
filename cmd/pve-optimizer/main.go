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

	"pve-optimizer/internal/advice"
	"pve-optimizer/internal/config"
	"pve-optimizer/internal/mode"
	"pve-optimizer/internal/proxmox"
	"pve-optimizer/internal/report"
	"pve-optimizer/internal/services"
	"pve-optimizer/internal/version"
	"pve-optimizer/internal/watcher"
)

func main() {
	configPath := flag.String("config", "/etc/pve-optimizer/config.yaml", "Pfad zur Konfigurationsdatei")
	showVersion := flag.Bool("version", false, "Version ausgeben und beenden")
	debug := flag.Bool("debug", false, "ausführliche Protokollierung")
	sweep := flag.Bool("sweep", false, "einmalig alle vorhandenen VMs anpassen und beenden")
	check := flag.Bool("check", false, "Empfehlungen prüfen, Befunde ausgeben und beenden")
	flag.Parse()

	if *showVersion {
		fmt.Printf("pve-optimizer %s (Build %s, %s)\n", version.Version, version.Build, version.BuiltAt)
		return
	}

	if err := run(*configPath, *debug, *sweep, *check); err != nil {
		slog.Error("dienst beendet", "fehler", err)
		os.Exit(1)
	}
}

func run(configPath string, debug, sweep, check bool) error {
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

	if check {
		return runChecks(ctx, cfg, client)
	}

	log.Info("pve-optimizer gestartet", "version", version.Version, "build", version.Build)
	if sweep {
		return w.Sweep(ctx)
	}
	if err := startMonitor(ctx, cfg, log); err != nil {
		return err
	}
	if err := startReport(ctx, cfg, client, log); err != nil {
		return err
	}
	return w.Run(ctx)
}

// startReport stellt den regelmäßigen Bericht daneben, sofern er
// eingeschaltet ist und dieser Node ihn verschicken soll.
func startReport(ctx context.Context, cfg *config.Config, client proxmox.Client, log *slog.Logger) error {
	settings, err := cfg.ReportFor(cfg.MonitoredNode())
	if err != nil {
		return err
	}
	if settings.Mode == mode.Off {
		return nil
	}

	checks, err := cfg.AdviceFor(cfg.MonitoredNode())
	if err != nil {
		return err
	}
	sender, err := cfg.Sender(cfg.MonitoredNode())
	if err != nil {
		return err
	}

	reporter, err := report.New(
		settings, cfg.MonitoredNode(), cfg.ReportStateFile(), checks, client, sender, log)
	if err != nil {
		return err
	}
	if !reporter.Responsible() {
		// Der Bericht gehört einem anderen Node. Das ist der Normalfall
		// bei einer Installation je Node und kein Fehler — es soll nur
		// nicht wie ein vergessener Schalter aussehen.
		log.Info("bericht übernimmt ein anderer node", "zuständig", settings.Node)
		return nil
	}

	go reporter.Run(ctx, cfg.PollInterval)
	return nil
}

// runChecks lässt die Empfehlungen einmal laufen und schreibt die Befunde
// nach stdout — für den Aufruf von Hand. Bewusst kein Protokollformat:
// Das hier liest ein Mensch.
func runChecks(ctx context.Context, cfg *config.Config, client proxmox.Client) error {
	set, err := cfg.AdviceFor(cfg.MonitoredNode())
	if err != nil {
		return err
	}

	findings, problems := set.Run(ctx, client)
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, "nicht geprüft:", problem)
	}

	if len(findings) == 0 {
		fmt.Println("Keine Befunde — alles Geprüfte sieht gut aus.")
		return nil
	}

	fmt.Printf("%d Befund(e):\n", len(findings))
	for _, group := range advice.Group(findings) {
		fmt.Printf("\n%s\n", group.Check)
		for _, subject := range group.Subjects {
			fmt.Printf("  - %s\n", subject)
		}
		fmt.Printf("\n  Warum:   %s\n  Was tun: %s\n", group.Why, group.Action)
	}

	// Befunde sind kein Fehler des Programms: Der Aufruf hat getan, was er
	// sollte. Ein Fehlschlag-Status käme in Skripten als Störung an.
	if len(problems) > 0 {
		return fmt.Errorf("%d prüfung(en) konnten nicht laufen", len(problems))
	}
	return nil
}

// startMonitor stellt den Dienst-Monitor daneben, sofern er eingeschaltet
// ist. Er läuft eigenständig: Ein abgestürztes pvestatd soll auch dann
// wieder hochkommen, wenn gerade die Aufgabenliste klemmt.
func startMonitor(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	settings, err := cfg.ServicesFor(cfg.MonitoredNode())
	if err != nil {
		return err
	}
	if settings.Mode == mode.Off {
		return nil
	}

	sender, err := cfg.Sender(cfg.MonitoredNode())
	if err != nil {
		return err
	}
	monitor, err := services.New(settings, cfg.MonitoredNode(), cfg.ServiceStateFile(), sender, log)
	if err != nil {
		return err
	}
	go monitor.Run(ctx, cfg.PollInterval)
	return nil
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
