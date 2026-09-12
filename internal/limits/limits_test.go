package limits

import (
	"strings"
	"testing"
)

func TestParseDisk(t *testing.T) {
	tests := []struct {
		name        string
		key, value  string
		wantOK      bool
		wantStorage string
		wantCDROM   bool
	}{
		{"scsi mit optionen", "scsi0", "local-pool:vm-100-disk-0,size=32G,iothread=1", true, "local-pool", false},
		{"virtio schlicht", "virtio1", "rpool:vm-101-disk-0,size=8G", true, "rpool", false},
		{"sata", "sata2", "nfs-store:vm-102-disk-0,size=1T", true, "nfs-store", false},
		{"cdrom wird erkannt", "ide2", "local:iso/debian.iso,media=cdrom", true, "local", true},
		{"efidisk ist keine nutzlast", "efidisk0", "local-pool:vm-100-disk-1,size=4M", false, "", false},
		{"tpmstate ist keine nutzlast", "tpmstate0", "local-pool:vm-100-disk-2,size=4M", false, "", false},
		{"unused wird ignoriert", "unused0", "local-pool:vm-100-disk-9", false, "", false},
		{"netzwerkkarte ist keine platte", "net0", "virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0", false, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			disk, ok := ParseDisk(tc.key, tc.value)
			if ok != tc.wantOK {
				t.Fatalf("ParseDisk(%q) ok = %v, erwartet %v", tc.key, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got := disk.Storage(); got != tc.wantStorage {
				t.Errorf("Storage() = %q, erwartet %q", got, tc.wantStorage)
			}
			if got := disk.IsCDROM(); got != tc.wantCDROM {
				t.Errorf("IsCDROM() = %v, erwartet %v", got, tc.wantCDROM)
			}
		})
	}
}

func TestParseDiskFlagOhneWert(t *testing.T) {
	disk, ok := ParseDisk("scsi0", "local-pool:vm-100-disk-0,size=32G,backup")
	if !ok {
		t.Fatal("ParseDisk sollte den Eintrag annehmen")
	}
	if _, found := disk.Options["backup"]; !found {
		t.Error("Flag ohne Wert wurde nicht uebernommen")
	}
	if disk.Options["size"] != "32G" {
		t.Errorf("size = %q, erwartet \"32G\"", disk.Options["size"])
	}
}

func TestMissing(t *testing.T) {
	profile := Profile{"mbps_rd": 200, "mbps_wr": 150, "iops_rd": 5000, "iops_wr": 0}

	tests := []struct {
		name  string
		value string
		want  map[string]int
	}{
		{
			name:  "platte ganz ohne begrenzung",
			value: "local-pool:vm-100-disk-0,size=32G",
			want:  map[string]int{"mbps_rd": 200, "mbps_wr": 150, "iops_rd": 5000},
		},
		{
			name:  "teilweise gesetzt, rest wird ergaenzt",
			value: "local-pool:vm-100-disk-0,size=32G,mbps_rd=50",
			want:  map[string]int{"mbps_wr": 150, "iops_rd": 5000},
		},
		{
			name:  "vollstaendig gesetzt",
			value: "local-pool:vm-100-disk-0,size=32G,mbps_rd=50,mbps_wr=40,iops_rd=100",
			want:  map[string]int{},
		},
		{
			name:  "bestehender wert wird nicht ueberschrieben",
			value: "local-pool:vm-100-disk-0,size=32G,mbps_rd=999999",
			want:  map[string]int{"mbps_wr": 150, "iops_rd": 5000},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			disk, ok := ParseDisk("scsi0", tc.value)
			if !ok {
				t.Fatal("ParseDisk sollte den Eintrag annehmen")
			}
			got := Missing(disk, profile)
			if len(got) != len(tc.want) {
				t.Fatalf("Missing() = %v, erwartet %v", got, tc.want)
			}
			for key, want := range tc.want {
				if got[key] != want {
					t.Errorf("Missing()[%q] = %d, erwartet %d", key, got[key], want)
				}
			}
		})
	}
}

func TestApply(t *testing.T) {
	disk, ok := ParseDisk("scsi0", "local-pool:vm-100-disk-0,size=32G,iothread=1")
	if !ok {
		t.Fatal("ParseDisk sollte den Eintrag annehmen")
	}

	got := Apply(disk, map[string]int{"mbps_rd": 200, "iops_wr": 3000})

	if !strings.HasPrefix(got, "local-pool:vm-100-disk-0,") {
		t.Errorf("Volume muss zuerst stehen, bekommen: %q", got)
	}
	for _, want := range []string{"size=32G", "iothread=1", "mbps_rd=200", "iops_wr=3000"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q fehlt in %q", want, got)
		}
	}
}

func TestApplyIstStabil(t *testing.T) {
	disk, _ := ParseDisk("scsi0", "local-pool:vm-100-disk-0,size=32G,iothread=1,ssd=1,discard=on")
	additions := map[string]int{"mbps_rd": 200, "mbps_wr": 150}

	first := Apply(disk, additions)
	for i := 0; i < 20; i++ {
		if got := Apply(disk, additions); got != first {
			t.Fatalf("Apply ist nicht stabil: %q vs. %q", got, first)
		}
	}
}
