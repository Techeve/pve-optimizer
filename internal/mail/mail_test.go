package mail

import (
	"strings"
	"testing"
	"time"
)

func TestOhneMailserverBleibtDieMeldungImProtokoll(t *testing.T) {
	sender := NewSender(Settings{}, "vmh03")
	if _, ok := sender.(LogOnly); !ok {
		t.Fatalf("ohne zugang wurde %T gewählt", sender)
	}
	if err := sender.Send("betreff", "text"); err != nil {
		t.Errorf("unerwarteter fehler: %v", err)
	}
}

func TestHalbEingerichteteMailWirdAbgewiesen(t *testing.T) {
	tests := map[string]struct {
		mail Settings
		want string
	}{
		"gar nichts":        {Settings{}, ""},
		"vollständig":       {Settings{To: "a@b.de", From: "c@d.de", Server: "mail:25"}, ""},
		"absender optional": {Settings{To: "a@b.de", Server: "mail:25"}, ""},
		"ohne empfänger":    {Settings{From: "c@d.de", Server: "mail:25"}, "to"},
		"ohne server":       {Settings{To: "a@b.de", From: "c@d.de"}, "server"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := test.mail.Check("vmh03")
			if test.want == "" {
				if err != nil {
					t.Fatalf("unerwarteter fehler: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("fehler = %v, erwartet ein hinweis auf %q", err, test.want)
			}
		})
	}
}

func TestNachrichtKopfzeilen(t *testing.T) {
	sender := &smtpSender{
		settings: Settings{To: "admin@example.com", Server: "mail:25"},
		from:     "pve@vmh03",
		now:      func() time.Time { return time.Unix(1789684658, 0).UTC() },
	}

	message := string(sender.compose("vmh03: pvestatd.service ist 3-mal abgestürzt", "Zeile eins\nZeile zwei\n"))

	for _, want := range []string{
		"From: pve@vmh03\r\n",
		"To: admin@example.com\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"Zeile eins\r\nZeile zwei\r\n",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("in der nachricht fehlt %q", want)
		}
	}
	if !strings.Contains(message, "Subject: =?utf-8?q?") {
		t.Errorf("betreff mit umlaut nicht kodiert: %q", message)
	}
}

func TestOhneBenutzerKeineAnmeldung(t *testing.T) {
	// net/smtp gibt ein Passwort nur über eine verschlüsselte Verbindung
	// heraus — ein Relay im eigenen Netz käme sonst gar nicht zustande.
	sender := &smtpSender{settings: Settings{Server: "mail:25"}}
	if sender.auth() != nil {
		t.Fatal("ohne benutzernamen wurde eine anmeldung aufgebaut")
	}
}

func TestAbsenderAusDemRechnernamen(t *testing.T) {
	// Ein Pflichtfeld, das immer gleich ausgefüllt wird, ist nur eine
	// Hürde — und im Posteingang soll stehen, von welchem Node es kam.
	settings := Settings{To: "a@b.de", Server: "mail:25"}
	if got := settings.from("vmh03"); got != "pve-optimizer@vmh03" {
		t.Errorf("from = %q, erwartet die Adresse aus dem Rechnernamen", got)
	}
	if got := (Settings{From: "eigen@b.de"}).from("vmh03"); got != "eigen@b.de" {
		t.Errorf("from = %q, eine eingetragene Adresse hat Vorrang", got)
	}
}
