package services

import (
	"testing"
	"time"
)

func TestZustandLesen(t *testing.T) {
	// Wortlaut von "systemctl show" auf einem Proxmox-Node, systemd 257.
	tests := map[string]struct {
		out  string
		want status
	}{
		"läuft": {
			out: "Result=success\nActiveState=active\nSubState=running\n" +
				"LoadState=loaded\nActiveEnterTimestamp=@1789684658\n",
			want: status{Known: true, Active: true, Since: time.Unix(1789684658, 0)},
		},
		"abgestürzt": {
			out:  "Result=signal\nActiveState=failed\nLoadState=loaded\nActiveEnterTimestamp=\n",
			want: status{Known: true, Failed: true},
		},
		"von hand angehalten": {
			out:  "Result=success\nActiveState=inactive\nLoadState=loaded\nActiveEnterTimestamp=\n",
			want: status{Known: true},
		},
		"gibt es nicht": {
			out:  "ActiveState=inactive\nLoadState=not-found\nActiveEnterTimestamp=\n",
			want: status{},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseStatus(test.out)
			if err != nil {
				t.Fatalf("unerwarteter fehler: %v", err)
			}
			if got != test.want {
				t.Errorf("zustand = %+v, erwartet %+v", got, test.want)
			}
		})
	}
}

func TestUnlesbarerZeitstempel(t *testing.T) {
	// Ohne --timestamp=unix gibt systemctl ein Datum in der Sprache des
	// Systems aus. Das soll auffallen und nicht als Laufzeit 1970 durchgehen.
	_, err := parseStatus("LoadState=loaded\nActiveState=active\n" +
		"ActiveEnterTimestamp=Thu 2026-09-18 00:37:38 CEST\n")
	if err == nil {
		t.Fatal("ein unlesbarer zeitstempel wurde stillschweigend angenommen")
	}
}
