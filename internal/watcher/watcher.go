// Package watcher beobachtet die Proxmox-Aufgaben und ergänzt nach jeder
// neu angelegten oder wiederhergestellten VM die fehlenden IO-Begrenzungen.
package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"pve-optimizer/internal/config"
	"pve-optimizer/internal/limits"
	"pve-optimizer/internal/proxmox"
)

// Aufgabentypen, nach denen eine VM neue Platten haben kann. Container
// bleiben außen vor: Proxmox kennt für LXC keine Drosselung je Mountpoint.
var relevantTasks = map[string]bool{
	"qmcreate":  true,
	"qmrestore": true,
	"qmclone":   true,
}

type Watcher struct {
	cfg    *config.Config
	client proxmox.Client
	log    *slog.Logger
	state  *State
}

func New(cfg *config.Config, client proxmox.Client, log *slog.Logger) (*Watcher, error) {
	state, err := LoadState(cfg.StateFile)
	if err != nil {
		return nil, err
	}
	return &Watcher{cfg: cfg, client: client, log: log, state: state}, nil
}

// Run beobachtet bis zum Abbruch des Kontexts.
func (w *Watcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	scope := "ganzer cluster"
	if w.cfg.RestrictedToOwnNode() {
		scope = "nur node " + w.cfg.Node
	}
	w.log.Info("beobachtung gestartet",
		"modus", w.cfg.Mode, "umfang", scope,
		"intervall", w.cfg.PollInterval, "dry_run", w.cfg.DryRun)

	for {
		if err := w.checkOnce(ctx); err != nil {
			// Ein Aussetzer der API darf den Dienst nicht beenden.
			w.log.Error("durchlauf fehlgeschlagen", "fehler", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Sweep geht einmalig alle vorhandenen VMs durch und ergänzt fehlende
// Begrenzungen. Gedacht für den Rollout: Die laufende Beobachtung greift
// nur bei neuen Aufgaben, bestehende VMs blieben sonst ungedrosselt.
func (w *Watcher) Sweep(ctx context.Context) error {
	all, err := w.client.ListVMs(ctx)
	if err != nil {
		return err
	}

	var vms []proxmox.VM
	for _, vm := range all {
		if w.ownNode(vm.Node) {
			vms = append(vms, vm)
		}
	}

	w.log.Info("durchlauf über alle vorhandenen vms",
		"anzahl", len(vms), "im cluster", len(all),
		"nur eigener node", w.cfg.RestrictedToOwnNode(), "dry_run", w.cfg.DryRun)

	var touched, failed int
	for _, vm := range vms {
		log := w.log.With("node", vm.Node, "vmid", vm.VMID, "name", vm.Name)

		changed, err := w.applyLimits(ctx, vm.Node, vm.VMID, log)
		if err != nil {
			// Eine einzelne VM darf den Durchlauf nicht abbrechen.
			log.Error("vm nicht angepasst", "fehler", err)
			failed++
			continue
		}
		if changed > 0 {
			touched++
		}
	}

	w.log.Info("durchlauf abgeschlossen",
		"geprüft", len(vms), "angepasst", touched, "fehlgeschlagen", failed)
	if failed > 0 {
		return fmt.Errorf("%d von %d vms konnten nicht angepasst werden", failed, len(vms))
	}
	return nil
}

// checkOnce verarbeitet alle Aufgaben, die seit dem letzten Durchlauf
// fertig geworden sind.
func (w *Watcher) checkOnce(ctx context.Context) error {
	tasks, err := w.client.RecentTasks(ctx)
	if err != nil {
		return err
	}

	newest := w.state.LastEndTime
	var firstFailure int64

	for _, task := range tasks {
		if !w.isNew(task) {
			continue
		}
		if err := w.handleTask(ctx, task); err != nil {
			w.log.Error("aufgabe nicht verarbeitet", "upid", task.UPID, "fehler", err)
			if firstFailure == 0 || task.EndTime < firstFailure {
				firstFailure = task.EndTime
			}
			continue
		}
		if task.EndTime > newest {
			newest = task.EndTime
		}
	}

	// Nicht über eine fehlgeschlagene Aufgabe hinaus vorrücken. Sonst gilt
	// sie als erledigt und die VM bliebe dauerhaft ungedrosselt, obwohl der
	// Fehler nur vorübergehend war — etwa ein Aussetzer der API oder ein
	// kurzzeitig schreibgeschütztes /etc/pve ohne Quorum.
	if firstFailure > 0 && newest >= firstFailure {
		newest = firstFailure - 1
	}

	if newest <= w.state.LastEndTime {
		return nil
	}
	w.state.LastEndTime = newest
	return w.state.Save(w.cfg.StateFile)
}

// isNew filtert auf abgeschlossene, für uns relevante und noch nicht
// gesehene Aufgaben.
func (w *Watcher) isNew(task proxmox.Task) bool {
	return task.Finished() &&
		relevantTasks[task.Type] &&
		task.EndTime > w.state.LastEndTime &&
		w.ownNode(task.Node)
}

// ownNode meldet, ob der Node bearbeitet werden darf. Läuft der Dienst auf
// jedem Node, kümmert sich jede Instanz nur um ihren eigenen — sonst würden
// sich mehrere gleichzeitig dieselbe VM vornehmen.
func (w *Watcher) ownNode(node string) bool {
	return !w.cfg.RestrictedToOwnNode() || node == w.cfg.Node
}

func (w *Watcher) handleTask(ctx context.Context, task proxmox.Task) error {
	vmid, ok := task.VMID()
	if !ok {
		return fmt.Errorf("aufgabe %s hat keine auswertbare vmid %q", task.Type, task.ID)
	}

	log := w.log.With("node", task.Node, "vmid", vmid, "aufgabe", task.Type)
	log.Info("vm fertiggestellt, platten werden geprüft")

	changed, err := w.applyLimits(ctx, task.Node, vmid, log)
	if err != nil {
		return err
	}
	if changed == 0 {
		log.Info("alle platten bereits begrenzt")
		return nil
	}
	log.Info("begrenzungen ergänzt", "platten", changed)
	return nil
}

// applyLimits ergänzt die fehlenden Begrenzungen aller Platten einer VM und
// liefert, wie viele Platten geändert wurden.
func (w *Watcher) applyLimits(ctx context.Context, node string, vmid int, log *slog.Logger) (int, error) {
	vmConfig, err := w.client.VMConfig(ctx, node, vmid)
	if err != nil {
		return 0, err
	}

	fields := map[string]string{}
	for key, value := range vmConfig {
		disk, ok := limits.ParseDisk(key, value)
		if !ok || disk.IsCDROM() {
			continue
		}

		profile, source := w.cfg.ProfileFor(node, disk.Storage())
		missing := limits.Missing(disk, profile)
		if len(missing) == 0 {
			continue
		}

		log.Info("platte wird begrenzt",
			"platte", key, "pool", disk.Storage(), "profil", source, "ergänzt", missing)
		fields[key] = limits.Apply(disk, missing)
	}

	if len(fields) == 0 {
		return 0, nil
	}
	if w.cfg.DryRun {
		log.Info("dry_run aktiv, es wird nichts geschrieben", "platten", len(fields))
		return len(fields), nil
	}
	if err := w.client.UpdateVMConfig(ctx, node, vmid, fields); err != nil {
		return 0, err
	}
	return len(fields), nil
}
