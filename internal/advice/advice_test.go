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
	nodes   []proxmox.Node
	pools   map[string][]proxmox.ZFSPool
	zustand map[string]proxmox.ZFSStatus
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
func (f *fakeCluster) Nodes(context.Context) ([]proxmox.Node, error) {
	return f.nodes, f.fehler
}
func (f *fakeCluster) ZFSPools(_ context.Context, node string) ([]proxmox.ZFSPool, error) {
	return f.pools[node], f.fehler
}
func (f *fakeCluster) ZFSPoolStatus(_ context.Context, node, pool string) (proxmox.ZFSStatus, error) {
	return f.zustand[node+"/"+pool], f.fehler
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
	// Über Names() statt über eine handgeschriebene Liste: Sonst
	// vergisst der Test die nächste neue Prüfung stillschweigend.
	var alleAus strings.Builder
	for _, name := range Names() {
		alleAus.WriteString(name + ": off\n")
	}
	set := build(t, alleAus.String(), "")
	findings, problems := set.Run(context.Background(), &fakeCluster{fehler: errors.New("API weg")})

	if len(findings) != 0 || len(problems) != 0 {
		t.Fatalf("Befunde = %v, Probleme = %v, erwartet beides leer", findings, problems)
	}
}

func TestEinScheiterndePruefungHaeltDieUebrigenNichtAuf(t *testing.T) {
	// Nur die Replikation liefert Daten, alles andere scheitert — der
	// Befund daraus muss trotzdem herauskommen.
	cluster := &fehlerhafterCluster{jobs: []proxmox.ReplicationJob{{ID: "100-0"}}}

	set := build(t, "", "")
	findings, problems := set.Run(context.Background(), cluster)
	if len(findings) != 1 {
		t.Errorf("Befunde = %+v, erwartet den einen aus der Replikation", findings)
	}
	if len(problems) != len(set)-1 {
		t.Errorf("Probleme = %d, erwartet %d — alle außer der Replikation", len(problems), len(set)-1)
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
func (c *fehlerhafterCluster) Nodes(context.Context) ([]proxmox.Node, error) {
	return nil, errors.New("API weg")
}
func (c *fehlerhafterCluster) ZFSPools(context.Context, string) ([]proxmox.ZFSPool, error) {
	return nil, errors.New("API weg")
}
func (c *fehlerhafterCluster) ZFSPoolStatus(context.Context, string, string) (proxmox.ZFSStatus, error) {
	return proxmox.ZFSStatus{}, errors.New("API weg")
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

func zfsCluster(nodes []proxmox.Node, pools map[string][]proxmox.ZFSPool, zustand map[string]proxmox.ZFSStatus) *fakeCluster {
	return &fakeCluster{nodes: nodes, pools: pools, zustand: zustand}
}

func platte(name string, read, write, cksum int64) proxmox.ZFSVdev {
	return proxmox.ZFSVdev{Name: name, State: "ONLINE", Leaf: 1, Read: read, Write: write, Cksum: cksum}
}

// gesund baut den Zustand eines Pools ohne Redundanz und ohne Fehler.
func gesund(name string) proxmox.ZFSStatus {
	return proxmox.ZFSStatus{
		Name: name, State: "ONLINE", Errors: "No known data errors",
		Children: []proxmox.ZFSVdev{{Name: name, Children: []proxmox.ZFSVdev{platte("nvme0n1", 0, 0, 0)}}},
	}
}

func TestZfsFehlerWerdenGemeldet(t *testing.T) {
	krank := gesund("rpool")
	krank.Children[0].Children[0] = platte("nvme0n1", 0, 0, 67)

	cluster := zfsCluster(
		[]proxmox.Node{{Name: "pve01", Status: "online"}},
		map[string][]proxmox.ZFSPool{"pve01": {{Name: "rpool"}}},
		map[string]proxmox.ZFSStatus{"pve01/rpool": krank},
	)

	findings := run(t, build(t, "", ""), "zfs_health", cluster)
	if len(findings) != 1 {
		t.Fatalf("Befunde = %+v, erwartet 1", findings)
	}
	if !strings.Contains(findings[0].Subject, "67") {
		t.Errorf("Subject = %q, erwartet den Fehlerzähler", findings[0].Subject)
	}
}

// Der Querbefund: Zwei unabhängige Laufwerke fangen nicht gleichzeitig an,
// Daten zu verfälschen — dann liegt die Ursache oberhalb der Laufwerke.
func TestFehlerAufMehrerenPoolsDeutetAufDenNode(t *testing.T) {
	eins := gesund("rpool")
	eins.Children[0].Children[0] = platte("nvme0n1", 0, 0, 2)
	zwei := gesund("local-pool")
	zwei.Children[0].Children[0] = platte("nvme1n1", 0, 0, 67)

	cluster := zfsCluster(
		[]proxmox.Node{{Name: "pve01", Status: "online"}},
		map[string][]proxmox.ZFSPool{"pve01": {{Name: "rpool"}, {Name: "local-pool"}}},
		map[string]proxmox.ZFSStatus{"pve01/rpool": eins, "pve01/local-pool": zwei},
	)

	findings := run(t, build(t, "", ""), "zfs_health", cluster)
	if len(findings) != 3 {
		t.Fatalf("Befunde = %d, erwartet 2 Pools + 1 Querbefund", len(findings))
	}
	quer := findings[len(findings)-1]
	if !strings.Contains(quer.Why, "oberhalb der Laufwerke") {
		t.Errorf("der Querbefund fehlt: %+v", quer)
	}
}

func TestOfflineNodeWirdUebersprungen(t *testing.T) {
	// Weder als gesund noch als kaputt melden — es ist schlicht nichts bekannt.
	cluster := zfsCluster([]proxmox.Node{{Name: "pve03", Status: "offline"}}, nil, nil)

	if findings := run(t, build(t, "", ""), "zfs_health", cluster); len(findings) != 0 {
		t.Fatalf("Befunde = %+v, erwartet keine", findings)
	}
}

func TestRedundanz(t *testing.T) {
	gespiegelt := proxmox.ZFSStatus{
		Name: "rpool", State: "ONLINE", Errors: "No known data errors",
		Children: []proxmox.ZFSVdev{{Name: "rpool", Children: []proxmox.ZFSVdev{
			{Name: "mirror-0", Children: []proxmox.ZFSVdev{platte("a", 0, 0, 0), platte("b", 0, 0, 0)}},
		}}},
	}

	tests := map[string]struct {
		status  proxmox.ZFSStatus
		befunde int
	}{
		"einzelne Platte": {gesund("rpool"), 1},
		"Spiegel":         {gespiegelt, 0},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cluster := zfsCluster(
				[]proxmox.Node{{Name: "pve01", Status: "online"}},
				map[string][]proxmox.ZFSPool{"pve01": {{Name: "rpool"}}},
				map[string]proxmox.ZFSStatus{"pve01/rpool": test.status},
			)
			findings := run(t, build(t, "", ""), "zfs_redundancy", cluster)
			if len(findings) != test.befunde {
				t.Fatalf("Befunde = %d, erwartet %d", len(findings), test.befunde)
			}
		})
	}
}

func TestSpeicherUeberbuchung(t *testing.T) {
	const gib = int64(1024 * 1024 * 1024)

	cluster := &fakeCluster{
		nodes: []proxmox.Node{
			{Name: "pve01", Status: "online", MaxMem: 64 * gib},
			{Name: "pve02", Status: "online", MaxMem: 64 * gib},
		},
		guests: []proxmox.Guest{
			{VMID: 100, Node: "pve01", MaxMem: 60 * gib, Status: "running"},
			// Gestoppte Gäste und Vorlagen belegen nichts.
			{VMID: 101, Node: "pve02", MaxMem: 60 * gib, Status: "stopped"},
			{VMID: 102, Node: "pve02", MaxMem: 60 * gib, Status: "running", Template: 1},
		},
	}

	findings := run(t, build(t, "", ""), "memory_overcommit", cluster)
	if len(findings) != 1 {
		t.Fatalf("Befunde = %+v, erwartet nur pve01", findings)
	}
	if !strings.Contains(findings[0].Subject, "pve01") || !strings.Contains(findings[0].Subject, "93 %") {
		t.Errorf("Subject = %q, erwartet pve01 mit Anteil", findings[0].Subject)
	}
}

func TestUnsinnigeGrenzeWirdAbgewiesen(t *testing.T) {
	if _, err := Build(settings(t, "memory_overcommit:\n  max_percent: 0\n"), nil); err == nil {
		t.Fatal("Build() nahm eine Grenze von 0 an")
	}
}
