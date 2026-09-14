// Package watcher beobachtet die Proxmox-Aufgaben und lässt nach jeder neu
// angelegten oder wiederhergestellten VM die eingeschalteten Regeln über
// sie laufen.
package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"pve-optimizer/internal/config"
	"pve-optimizer/internal/limits"
	"pve-optimizer/internal/proxmox"
	"pve-optimizer/internal/rules"
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

	// rules je Node. Die Regeln eines Nodes ändern sich zur Laufzeit nicht,
	// bei einem Durchlauf über hunderte VMs lohnt sich das Merken.
	rules map[string]rules.Set
}

func New(cfg *config.Config, client proxmox.Client, log *slog.Logger) (*Watcher, error) {
	state, err := LoadState(cfg.StateFile)
	if err != nil {
		return nil, err
	}
	return &Watcher{
		cfg: cfg, client: client, log: log, state: state,
		rules: map[string]rules.Set{},
	}, nil
}

// rulesFor liefert die Regeln eines Nodes. Fehler sind hier nicht mehr zu
// erwarten: Die Konfiguration ist beim Start für jeden genannten Node
// durchgebaut worden.
func (w *Watcher) rulesFor(node string) (rules.Set, error) {
	if set, found := w.rules[node]; found {
		return set, nil
	}
	set, err := w.cfg.RulesFor(node)
	if err != nil {
		return nil, err
	}
	w.rules[node] = set
	return set, nil
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
		"intervall", w.cfg.PollInterval, "regeln", w.modes())

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
		"nur eigener node", w.cfg.RestrictedToOwnNode(), "regeln", w.modes())

	var touched, failed int
	for _, vm := range vms {
		log := w.log.With("node", vm.Node, "vmid", vm.VMID, "name", vm.Name)

		changed, err := w.applyRules(ctx, vm.Node, vm.VMID, log)
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
	log.Info("vm fertiggestellt, regeln werden geprüft")

	changed, err := w.applyRules(ctx, task.Node, vmid, log)
	if err != nil {
		return err
	}
	if changed == 0 {
		log.Info("nichts zu ergänzen")
		return nil
	}
	log.Info("konfiguration ergänzt", "felder", changed)
	return nil
}

// applyRules lässt die Regeln des Nodes über eine VM laufen und liefert,
// wie viele Felder ihrer Konfiguration geändert wurden.
func (w *Watcher) applyRules(ctx context.Context, node string, vmid int, log *slog.Logger) (int, error) {
	set, err := w.rulesFor(node)
	if err != nil {
		return 0, err
	}
	vmConfig, err := w.client.VMConfig(ctx, node, vmid)
	if err != nil {
		return 0, err
	}

	plan := rules.NewPlan(node, vmid, vmConfig, func(storage string) (limits.Profile, string) {
		return w.cfg.ProfileFor(node, storage)
	})
	set.Apply(plan)
	logNotes(log, plan.Notes())

	fields := plan.Fields()
	if len(fields) == 0 {
		return 0, nil
	}
	if err := w.client.UpdateVMConfig(ctx, node, vmid, fields); err != nil {
		return 0, err
	}
	return len(fields), nil
}

// logNotes schreibt, was die Regeln vorhaben. Übersprungenes steht nur im
// ausführlichen Protokoll — es ist der Normalfall und würde die Ausgabe
// sonst zuschütten.
func logNotes(log *slog.Logger, notes []rules.Note) {
	for _, note := range notes {
		entry := log.With("regel", note.Rule, "feld", note.Key)
		switch note.Status {
		case rules.StatusSkipped:
			entry.Debug("regel greift nicht", "grund", note.Context)
		case rules.StatusReported:
			entry.Info("würde ergänzt", "wert", note.Change, "herkunft", note.Context)
		default:
			entry.Info("wird ergänzt", "wert", note.Change, "herkunft", note.Context)
		}
	}
}

// modes nennt die Regeln mit ihrem Modus — beim Start soll im Protokoll
// stehen, was scharf ist und was nur meldet. Genannt wird der Stand für
// den eigenen Node; abweichende Nodes stehen daneben, damit klar ist, dass
// anderswo etwas anderes gilt.
func (w *Watcher) modes() string {
	set, err := w.rulesFor(w.cfg.Node)
	if err != nil {
		return "nicht ermittelbar: " + err.Error()
	}

	modes := set.Modes()
	if others := w.nodesWithOwnRules(); others != "" {
		modes += " (eigene regeln: " + others + ")"
	}
	return modes
}

func (w *Watcher) nodesWithOwnRules() string {
	var nodes []string
	for node, settings := range w.cfg.Nodes {
		if node != w.cfg.Node && len(settings.Rules) > 0 {
			nodes = append(nodes, node)
		}
	}
	sort.Strings(nodes)
	return strings.Join(nodes, ", ")
}
