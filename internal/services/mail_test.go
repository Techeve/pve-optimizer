package services

import (
	"strings"
	"testing"
	"time"

	"pve-optimizer/internal/mode"
)

func TestOhneMailserverBleibtDieMeldungImProtokoll(t *testing.T) {
	sender := Mail{}.Sender()
	if _, ok := sender.(logOnly); !ok {
		t.Fatalf("ohne zugang wurde %T gewählt", sender)
	}
	if err := sender.Send("betreff", "text"); err != nil {
		t.Errorf("unerwarteter fehler: %v", err)
	}
}

func TestHalbEingerichteteMailWirdAbgewiesen(t *testing.T) {
	tests := map[string]struct {
		mail Mail
		want string
	}{
		"gar nichts":     {Mail{}, ""},
		"vollständig":    {Mail{To: "a@b.de", From: "c@d.de", Server: "mail:25"}, ""},
		"ohne empfänger": {Mail{From: "c@d.de", Server: "mail:25"}, "to"},
		"ohne server":    {Mail{To: "a@b.de", From: "c@d.de"}, "server"},
		"nur empfänger":  {Mail{To: "a@b.de"}, "from, server"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := test.mail.Check()
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
		mail: Mail{To: "admin@techeve.de", From: "pve@vmh03", Server: "mail:25"},
		now:  func() time.Time { return time.Unix(1789684658, 0).UTC() },
	}

	message := string(sender.compose("vmh03: pvestatd.service ist 3-mal abgestürzt", "Zeile eins\nZeile zwei\n"))

	for _, want := range []string{
		"From: pve@vmh03\r\n",
		"To: admin@techeve.de\r\n",
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
	sender := &smtpSender{mail: Mail{Server: "mail:25"}}
	if sender.auth() != nil {
		t.Fatal("ohne benutzernamen wurde eine anmeldung aufgebaut")
	}
}

func TestEinstellungenPruefen(t *testing.T) {
	scharf := func(anpassen func(*Settings)) Settings {
		settings := DefaultSettings()
		settings.Mode = mode.Enforce
		anpassen(&settings)
		return settings
	}

	tests := map[string]struct {
		settings Settings
		want     string
	}{
		"vorgabe ist aus":   {DefaultSettings(), ""},
		"scharf und gültig": {scharf(func(*Settings) {}), ""},
		"ohne unit":         {scharf(func(s *Settings) { s.Units = nil }), "units"},
		"leere unit":        {scharf(func(s *Settings) { s.Units = []string{""} }), "leeren eintrag"},
		"grenze null":       {scharf(func(s *Settings) { s.RestartLimit = 0 }), "restart_limit"},
		"frist zu kurz":     {scharf(func(s *Settings) { s.StableAfter = time.Minute }), "stable_after"},
		"mail halb":         {scharf(func(s *Settings) { s.Mail = Mail{To: "a@b.de"} }), "halb eingerichtet"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := test.settings.Check()
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
