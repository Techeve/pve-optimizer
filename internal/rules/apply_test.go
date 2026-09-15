package rules

import (
	"strings"
	"testing"

	"pve-optimizer/internal/limits"
	"pve-optimizer/internal/proxmox"
)

// newPlan baut den Entwurf für eine VM. Das Profil ist bewusst schmal —
// die Rechnerei dahinter prüft das Paket limits.
func newPlan(kind proxmox.Kind, config map[string]string) *Plan {
	return NewPlan("vmh02", kind, 100, config, func(string) (limits.Profile, string) {
		return limits.Profile{"mbps_rd": 200}, "defaults"
	})
}

func apply(t *testing.T, general string, config map[string]string) *Plan {
	t.Helper()
	return applyTo(t, proxmox.KindQemu, general, config)
}

func applyTo(t *testing.T, kind proxmox.Kind, general string, config map[string]string) *Plan {
	t.Helper()
	plan := newPlan(kind, config)
	build(t, general, "").Apply(plan)
	return plan
}

func TestIOLimitsErgaenztAusDemProfil(t *testing.T) {
	plan := apply(t, "", map[string]string{"scsi0": "local-pool:vm-100-disk-0,size=32G"})

	if got := plan.Fields()["scsi0"]; !strings.Contains(got, "mbps_rd=200") {
		t.Errorf("scsi0 = %q, erwartet die ergänzte Begrenzung", got)
	}
}

func TestDiscardUndSSD(t *testing.T) {
	tests := []struct {
		name     string
		general  string
		disk     string
		want     string
		wantNone bool
	}{
		{
			name:    "discard wird ergänzt",
			general: "discard: enforce\nio_limits: off\n",
			disk:    "nvme:vm-100-disk-0,size=32G",
			want:    "discard=on",
		},
		{
			name:     "fremder pool bleibt unangetastet",
			general:  "io_limits: off\ndiscard:\n  mode: enforce\n  pools: [nvme]\n",
			disk:     "rust:vm-100-disk-0,size=32G",
			wantNone: true,
		},
		{
			name:    "ssd-kennzeichen am scsi-bus",
			general: "ssd: enforce\nio_limits: off\n",
			disk:    "nvme:vm-100-disk-0,size=32G",
			want:    "ssd=1",
		},
		{
			name:     "bereits gesetztes discard bleibt",
			general:  "discard: enforce\nio_limits: off\n",
			disk:     "nvme:vm-100-disk-0,size=32G,discard=ignore",
			wantNone: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := apply(t, tc.general, map[string]string{"scsi0": tc.disk})

			got, written := plan.Fields()["scsi0"]
			switch {
			case tc.wantNone && written:
				t.Errorf("scsi0 = %q, erwartet keine Änderung", got)
			case !tc.wantNone && !strings.Contains(got, tc.want):
				t.Errorf("scsi0 = %q, erwartet %q darin", got, tc.want)
			}
		})
	}
}

// virtio-blk kennt kein SSD-Kennzeichen — Proxmox würde die Platte
// zurückweisen und damit die ganze Änderung scheitern lassen.
func TestSSDLaesstVirtioBlkInRuhe(t *testing.T) {
	plan := apply(t, "ssd: enforce\nio_limits: off\n",
		map[string]string{"virtio0": "nvme:vm-100-disk-0,size=32G"})

	if got, written := plan.Fields()["virtio0"]; written {
		t.Errorf("virtio0 = %q, erwartet keine Änderung", got)
	}
}

func TestIothread(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]string
		key    string
		want   bool
	}{
		{
			name:   "scsi mit virtio-scsi-single",
			config: map[string]string{"scsihw": "virtio-scsi-single", "scsi0": "nvme:vm-100-disk-0"},
			key:    "scsi0",
			want:   true,
		},
		{
			name:   "scsi am falschen controller",
			config: map[string]string{"scsihw": "lsi", "scsi0": "nvme:vm-100-disk-0"},
			key:    "scsi0",
		},
		{
			name:   "ohne angabe gilt der alte controller",
			config: map[string]string{"scsi0": "nvme:vm-100-disk-0"},
			key:    "scsi0",
		},
		{
			name:   "virtio-blk kann es immer",
			config: map[string]string{"virtio0": "nvme:vm-100-disk-0"},
			key:    "virtio0",
			want:   true,
		},
		{
			name:   "sata kann es nie",
			config: map[string]string{"sata0": "nvme:vm-100-disk-0"},
			key:    "sata0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := apply(t, "iothread: enforce\nio_limits: off\n", tc.config)

			got, written := plan.Fields()[tc.key]
			if written != tc.want {
				t.Errorf("%s = %q, geschrieben: %v, erwartet: %v", tc.key, got, written, tc.want)
			}
			if tc.want && !strings.Contains(got, "iothread=1") {
				t.Errorf("%s = %q, erwartet iothread=1 darin", tc.key, got)
			}
		})
	}
}

func TestGuestAgent(t *testing.T) {
	tests := []struct {
		name    string
		general string
		config  map[string]string
		want    string
	}{
		{"fehlt und wird ergänzt", "guest_agent: enforce\n", map[string]string{}, "1"},
		{
			name:    "mit aufräumen nach dem klonen",
			general: "guest_agent:\n  mode: enforce\n  fstrim_cloned_disks: true\n",
			config:  map[string]string{},
			want:    "1,fstrim_cloned_disks=1",
		},
		{
			name:    "bewusst abgeschaltet bleibt abgeschaltet",
			general: "guest_agent: enforce\n",
			config:  map[string]string{"agent": "0"},
			want:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := apply(t, tc.general+"io_limits: off\n", tc.config)

			if got := plan.Fields()["agent"]; got != tc.want {
				t.Errorf("agent = %q, erwartet %q", got, tc.want)
			}
		})
	}
}

func TestStartupStaffelung(t *testing.T) {
	general := "io_limits: off\nstartup:\n  mode: enforce\n  up: 30s\n"

	tests := []struct {
		name   string
		config map[string]string
		want   string
	}{
		{
			name:   "automatisch startende vm bekommt einen abstand",
			config: map[string]string{"onboot": "1"},
			want:   "up=30",
		},
		{
			name:   "reihenfolge des betreibers bleibt und wird ergänzt",
			config: map[string]string{"onboot": "1", "startup": "order=2"},
			want:   "order=2,up=30",
		},
		{
			name:   "eigener abstand bleibt unangetastet",
			config: map[string]string{"onboot": "1", "startup": "order=2,up=90"},
			want:   "",
		},
		{
			name:   "vm ohne autostart wird nicht angefasst",
			config: map[string]string{},
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := apply(t, general, tc.config)

			if got := plan.Fields()["startup"]; got != tc.want {
				t.Errorf("startup = %q, erwartet %q", got, tc.want)
			}
		})
	}
}

// Mehrere Regeln an derselben Platte müssen sich zu einem Wert
// zusammenfinden — geschrieben wird das Feld am Ende einmal.
func TestMehrereRegelnAnEinerPlatte(t *testing.T) {
	plan := apply(t, "discard: enforce\nssd: enforce\niothread: enforce\n",
		map[string]string{"scsihw": "virtio-scsi-single", "scsi0": "nvme:vm-100-disk-0,size=32G"})

	got := plan.Fields()["scsi0"]
	for _, want := range []string{"discard=on", "ssd=1", "iothread=1", "mbps_rd=200", "size=32G"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q fehlt in %q", want, got)
		}
	}
}

// Im Modus "report" soll alles im Protokoll stehen, aber nichts
// geschrieben werden.
func TestReportSchreibtNicht(t *testing.T) {
	plan := apply(t, "discard: report\nio_limits: off\n",
		map[string]string{"scsi0": "nvme:vm-100-disk-0,size=32G"})

	if len(plan.Fields()) != 0 {
		t.Errorf("Fields() = %v, erwartet nichts zu schreiben", plan.Fields())
	}

	var reported int
	for _, note := range plan.Notes() {
		if note.Status == StatusReported && note.Change == "discard=on" {
			reported++
		}
	}
	if reported != 1 {
		t.Errorf("%d vermerke im protokoll, erwartet genau einen", reported)
	}
}

// Was die Regel nicht anwenden kann, soll mit Grund im Protokoll stehen —
// sonst sucht man vergeblich, warum eine Platte unverändert bleibt.
func TestUebersprungenesStehtImProtokoll(t *testing.T) {
	plan := apply(t, "iothread: enforce\nio_limits: off\n",
		map[string]string{"scsihw": "lsi", "scsi0": "nvme:vm-100-disk-0"})

	for _, note := range plan.Notes() {
		if note.Status == StatusSkipped && strings.Contains(note.Context, "virtio-scsi-single") {
			return
		}
	}
	t.Errorf("kein vermerk über die übersprungene platte: %+v", plan.Notes())
}

// CD-ROMs und Sonderplatten wie efidisk nehmen keine Drosselung an.
func TestSonderplattenBleibenAussenVor(t *testing.T) {
	plan := apply(t, "discard: enforce\n", map[string]string{
		"ide2":      "local:iso/debian.iso,media=cdrom",
		"efidisk0":  "nvme:vm-100-disk-1,size=4M",
		"unused0":   "nvme:vm-100-disk-2",
		"tpmstate0": "nvme:vm-100-disk-3,size=4M",
	})

	if len(plan.Fields()) != 0 {
		t.Errorf("Fields() = %v, erwartet keine Änderung", plan.Fields())
	}
}

// Container bekommen ihren eigenen Abstand — sie sind in Sekunden oben,
// während eine VM erst ihr BIOS durchläuft.
func TestStartupJeGastart(t *testing.T) {
	general := "io_limits: off\nstartup:\n  mode: enforce\n  vm:\n    up: 45s\n  lxc:\n    up: 10s\n"

	tests := []struct {
		kind proxmox.Kind
		want string
	}{
		{proxmox.KindQemu, "up=45"},
		{proxmox.KindLXC, "up=10"},
	}

	for _, tc := range tests {
		t.Run(string(tc.kind), func(t *testing.T) {
			plan := applyTo(t, tc.kind, general, map[string]string{"onboot": "1"})

			if got := plan.Fields()["startup"]; got != tc.want {
				t.Errorf("startup = %q, erwartet %q", got, tc.want)
			}
		})
	}
}

// Eine ausgenommene Gastart bleibt unangetastet.
func TestStartupUebergehtAusgenommeneGastart(t *testing.T) {
	general := "io_limits: off\nstartup:\n  mode: enforce\n  up: 30s\n  lxc:\n    up: 0\n"
	plan := applyTo(t, proxmox.KindLXC, general, map[string]string{"onboot": "1"})

	if got, written := plan.Fields()["startup"]; written {
		t.Errorf("startup = %q, erwartet keine Änderung", got)
	}
}

// Alles außer der Staffelung gibt es nur bei VMs. Ein "agent" in einer
// Container-Konfiguration würde Proxmox zurückweisen — und damit die
// ganze Änderung scheitern lassen.
func TestNurStartupFasstContainerAn(t *testing.T) {
	general := "" +
		"io_limits: enforce\ndiscard: enforce\nssd: enforce\niothread: enforce\n" +
		"guest_agent: enforce\nstartup:\n  mode: enforce\n  up: 10s\n"

	plan := applyTo(t, proxmox.KindLXC, general, map[string]string{
		"onboot": "1",
		"rootfs": "local-zfs:subvol-200-disk-0,size=8G",
		"mp0":    "local-zfs:subvol-200-disk-1,mp=/daten,size=100G",
	})

	fields := plan.Fields()
	if len(fields) != 1 || fields["startup"] != "up=10" {
		t.Errorf("Fields() = %v, erwartet nur startup=up=10", fields)
	}
	for _, note := range plan.Notes() {
		if note.Rule != "startup" {
			t.Errorf("regel %q hat sich an einem container zu schaffen gemacht: %+v", note.Rule, note)
		}
	}
}
