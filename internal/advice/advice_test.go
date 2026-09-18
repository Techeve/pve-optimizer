package advice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"pve-optimizer/internal/mode"
	"pve-optimizer/internal/proxmox"
)

// fakeCluster ersetzt Proxmox im Test.
type fakeCluster struct {
	guests  []proxmox.Guest
	offen   []proxmox.Guest
	options proxmox.Options
	jobs    []proxmox.ReplicationJob
	fehler  error
}

func (f *fakeCluster) ListGuests(context.Context) ([]proxmox.Guest, error) {
	return f.guests, f.fehler
}
func (f *fakeCluster) NotBackedUp(context.Context) ([]proxmox.Guest, error) {
	return f.offen, f.fehler
}
func (f *fakeCluster) Options(context.Context) (proxmox.Options, error) {
	return f.options, f.fehler
}
func (f *fakeCluster) ReplicationJobs(context.Context) ([]proxmox.ReplicationJob, error) {
	return f.jobs, f.fehler
}

func settings(t *testing.T, text string) map[string]yaml.Node {
	t.Helper()
	if text == "" {
		return nil
	}
	var parsed map[string]yaml.Node
	if err := yaml.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("testkonfiguration lesen: %v", err)
	}
	return parsed
}

func build(t *testing.T, general, node string) Set {
	t.Helper()
	set, err := Build(settings(t, general), settings(t, node))
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	return set
}

// run lässt nur die genannte Prüfung laufen.
func run(t *testing.T, set Set, name string, cluster Cluster) []Finding {
	t.Helper()
	for _, check := range set {
		if check.Name() != name {
			continue
		}
		findings, err := check.Run(context.Background(), cluster)
		if err != nil {
			t.Fatalf("Run(%s) = %v", name, err)
		}
		return findings
	}
	t.Fatalf("prüfung %q fehlt im satz", name)
	return nil
}

func TestPruefungenSindVonHausAusAn(t *testing.T) {
	// Eine Empfehlung ändert nichts — was man nicht sieht, kann man nicht
	// abstellen.
	for _, check := range build(t, "", "") {
		if check.Mode() != mode.Report {
			t.Errorf("%s = %q, erwartet %q", check.Name(), check.Mode(), mode.Report)
		}
	}
}

func TestEnforceWirdAbgewiesen(t *testing.T) {
	_, err := Build(settings(t, "replication_rate: enforce\n"), nil)
	if err == nil || !strings.Contains(err.Error(), "ändern nichts") {
		t.Fatalf("Build() = %v, erwartet eine Absage an enforce", err)
	}
}

func TestTippfehlerWerdenAbgewiesen(t *testing.T) {
	tests := map[string]string{
		"unbekannte prüfung": "backup_coverrage: report\n",
		"unbekannte option":  "backup_coverage:\n  mode: report\n  ignoore: [1]\n",
	}
	for name, general := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Build(settings(t, general), nil); err == nil {
				t.Error("Build() = nil, erwartet einen Fehler")
			}
		})
	}
}

func TestNodeWeichtAb(t *testing.T) {
	set := build(t, "backup_coverage:\n  mode: report\n  ignore: [101]\n", "backup_coverage: off\n")
	for _, check := range set {
		if check.Name() == "backup_coverage" && check.Mode() != mode.Off {
			t.Errorf("Mode = %q, erwartet %q vom Node", check.Mode(), mode.Off)
		}
	}
}

func TestUngesicherteGaesteWerdenGenannt(t *testing.T) {
	cluster := &fakeCluster{
		guests: []proxmox.Guest{
			{VMID: 3000, Node: "vmh02", Name: "LCM-Test-PVE", Kind: proxmox.KindQemu},
			{VMID: 101, Node: "vmh03", Name: "LCM-Prox-test", Kind: proxmox.KindLXC},
		},
		offen: []proxmox.Guest{
			{VMID: 3000, Name: "LCM-Test-PVE", Kind: proxmox.KindQemu},
			{VMID: 101, Name: "LCM-Prox-test", Kind: proxmox.KindLXC},
		},
	}

	findings := run(t, build(t, "", ""), "backup_coverage", cluster)
	if len(findings) != 2 {
		t.Fatalf("Befunde = %d, erwartet 2", len(findings))
	}

	// Der Node steht nicht in der Antwort von Proxmox — er kommt aus der
	// Gastliste dazu.
	if !strings.Contains(findings[0].Subject, "vmh02") {
		t.Errorf("Subject = %q, erwartet den Node", findings[0].Subject)
	}
	if !strings.Contains(findings[1].Subject, "Container") {
		t.Errorf("Subject = %q, erwartet die Gastart Container", findings[1].Subject)
	}
	for _, f := range findings {
		if f.Why == "" || f.Action == "" {
			t.Errorf("Befund ohne Begründung oder Handlungsschritt: %+v", f)
		}
	}
}

func TestIgnorierteGaesteFallenRaus(t *testing.T) {
	cluster := &fakeCluster{
		guests: []proxmox.Guest{{VMID: 3000, Node: "vmh02", Kind: proxmox.KindQemu}},
		offen:  []proxmox.Guest{{VMID: 3000, Kind: proxmox.KindQemu}},
	}

	set := build(t, "backup_coverage:\n  mode: report\n  ignore: [3000]\n", "")
	if findings := run(t, set, "backup_coverage", cluster); len(findings) != 0 {
		t.Fatalf("Befunde = %v, erwartet keine", findings)
	}
}

func TestVerwaisterIgnoreEintragWirdGemeldet(t *testing.T) {
	// Proxmox vergibt gelöschte Nummern wieder — ein alter Eintrag würde
	// sonst irgendwann stillschweigend einen neuen Gast ausnehmen.
	cluster := &fakeCluster{guests: []proxmox.Guest{{VMID: 100, Node: "vmh01"}}}

	set := build(t, "backup_coverage:\n  mode: report\n  ignore: [999]\n", "")
	findings := run(t, set, "backup_coverage", cluster)
	if len(findings) != 1 || !strings.Contains(findings[0].Subject, "999") {
		t.Fatalf("Befunde = %+v, erwartet einen Hinweis auf 999", findings)
	}
}

func TestBandbreitenGrenzen(t *testing.T) {
	gesetzt := func(keys ...string) proxmox.Options {
		limits := map[string]json.RawMessage{}
		for _, key := range keys {
			limits[key] = json.RawMessage("102400")
		}
		return proxmox.Options{BandwidthLimits: limits}
	}

	tests := map[string]struct {
		options proxmox.Options
		befunde int
		nennt   string
	}{
		"gar nichts gesetzt":  {proxmox.Options{}, 1, "keine einzige"},
		"default deckt alles": {gesetzt("default"), 0, ""},
		"alle einzeln":        {gesetzt("restore", "migration", "move", "clone"), 0, ""},
		"restore fehlt":       {gesetzt("migration", "move", "clone"), 1, "restore"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			findings := run(t, build(t, "", ""), "bandwidth_limits", &fakeCluster{options: test.options})
			if len(findings) != test.befunde {
				t.Fatalf("Befunde = %d, erwartet %d: %+v", len(findings), test.befunde, findings)
			}
			if test.nennt != "" && !strings.Contains(findings[0].Subject, test.nennt) {
				t.Errorf("Subject = %q, erwartet einen Hinweis auf %q", findings[0].Subject, test.nennt)
			}
		})
	}
}

func TestReplikationOhneRate(t *testing.T) {
	cluster := &fakeCluster{jobs: []proxmox.ReplicationJob{
		{ID: "100-0", Source: "vmh01", Target: "vmh02"},
		{ID: "150-0", Source: "vmh01", Target: "vmh02", Rate: json.RawMessage("50")},
	}}

	findings := run(t, build(t, "", ""), "replication_rate", cluster)
	if len(findings) != 1 {
		t.Fatalf("Befunde = %d, erwartet 1", len(findings))
	}
	if !strings.Contains(findings[0].Subject, "100-0") {
		t.Errorf("Subject = %q, erwartet den ungebremsten Auftrag", findings[0].Subject)
	}
}

func TestAusgeschaltetePruefungLaeuftNicht(t *testing.T) {
	set := build(t, "backup_coverage: off\nbandwidth_limits: off\nreplication_rate: off\n", "")
	findings, problems := set.Run(context.Background(), &fakeCluster{fehler: errors.New("API weg")})

	if len(findings) != 0 || len(problems) != 0 {
		t.Fatalf("Befunde = %v, Probleme = %v, erwartet beides leer", findings, problems)
	}
}

func TestEinScheiterndePruefungHaeltDieUebrigenNichtAuf(t *testing.T) {
	// Nur die Replikation liefert Daten, alles andere scheitert — der
	// Befund daraus muss trotzdem herauskommen.
	cluster := &fehlerhafterCluster{jobs: []proxmox.ReplicationJob{{ID: "100-0"}}}

	findings, problems := build(t, "", "").Run(context.Background(), cluster)
	if len(findings) != 1 {
		t.Errorf("Befunde = %+v, erwartet den einen aus der Replikation", findings)
	}
	if len(problems) != 2 {
		t.Errorf("Probleme = %d, erwartet 2 gescheiterte Prüfungen", len(problems))
	}
}

// fehlerhafterCluster lässt nur die Replikation gelingen.
type fehlerhafterCluster struct {
	jobs []proxmox.ReplicationJob
}

func (c *fehlerhafterCluster) ListGuests(context.Context) ([]proxmox.Guest, error) {
	return nil, errors.New("API weg")
}
func (c *fehlerhafterCluster) NotBackedUp(context.Context) ([]proxmox.Guest, error) {
	return nil, errors.New("API weg")
}
func (c *fehlerhafterCluster) Options(context.Context) (proxmox.Options, error) {
	return proxmox.Options{}, errors.New("API weg")
}
func (c *fehlerhafterCluster) ReplicationJobs(context.Context) ([]proxmox.ReplicationJob, error) {
	return c.jobs, nil
}

func TestBefundeWerdenZusammengefasst(t *testing.T) {
	// Vier Gäste mit demselben Grund sollen die Erklärung einmal tragen,
	// nicht viermal.
	findings := []Finding{
		{Check: "backup_coverage", Subject: "VM 1", Why: "kein backup", Action: "hinzufügen"},
		{Check: "replication_rate", Subject: "Job A", Why: "ungebremst", Action: "rate setzen"},
		{Check: "backup_coverage", Subject: "VM 2", Why: "kein backup", Action: "hinzufügen"},
		// Dieselbe Prüfung, aber ein anderer Grund — bleibt getrennt.
		{Check: "backup_coverage", Subject: "VMID 999", Why: "verwaist", Action: "entfernen"},
	}

	groups := Group(findings)
	if len(groups) != 3 {
		t.Fatalf("Gruppen = %d, erwartet 3", len(groups))
	}

	// Feste Reihenfolge, damit sich zwei Berichte vergleichen lassen.
	if groups[0].Check != "backup_coverage" || groups[2].Check != "replication_rate" {
		t.Errorf("Reihenfolge = %q, %q, %q", groups[0].Check, groups[1].Check, groups[2].Check)
	}
	if len(groups[0].Subjects) != 2 {
		t.Errorf("erste Gruppe = %v, erwartet die beiden VMs", groups[0].Subjects)
	}
	if len(groups[1].Subjects) != 1 || groups[1].Why != "verwaist" {
		t.Errorf("zweite Gruppe = %+v, erwartet den verwaisten Eintrag allein", groups[1])
	}
}
