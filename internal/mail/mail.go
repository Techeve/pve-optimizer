// Package mail verschickt die Meldungen des Dienstes.
//
// Bewusst ein eigener SMTP-Zugang statt des sendmail des Nodes: Ein frisch
// aufgesetzter Proxmox-Node hat zwar ein postfix, aber keinen Relay und
// als Empfaengeradresse eine Platzhalteradresse. Eine Mail ueber diesen
// Weg landet in der Warteschlange und nie bei einem Menschen — und eine
// Warnung, die niemand bekommt, ist schlimmer als keine.
package mail

import (
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"sort"
	"strings"
	"time"
)

// Settings ist der Zugang zum Mailserver. Er gilt für den ganzen Dienst:
// Dienst-Monitor und Bericht teilen ihn sich, damit nicht zwei Kopien
// auseinanderlaufen.
type Settings struct {
	To   string `yaml:"to"`
	From string `yaml:"from"`
	// Server ist der Mailserver als "host:port".
	Server string `yaml:"server"`
	// Username bleibt leer, wenn der Server ohne Anmeldung annimmt — bei
	// einem Relay im eigenen Netz der Normalfall.
	Username string `yaml:"username"`
	// Password kommt aus der Umgebung, nicht aus der Konfigurationsdatei.
	Password string `yaml:"-"`
}

// Sender verschickt die Meldung.
type Sender interface {
	Send(subject, body string) error
	// Describe sagt in einem Halbsatz, wohin die Meldung geht. Steht beim
	// Start im Protokoll, damit niemand erst im Ernstfall merkt, dass
	// nichts eingerichtet ist.
	Describe() string
}

// Check prüft den Zugang. Gar nichts anzugeben ist erlaubt — dann bleibt
// die Meldung im Protokoll. Halb ausgefüllt ist dagegen fast immer ein
// Versehen.
func (s Settings) Check(host string) error {
	// "from" darf fehlen — die Absenderadresse lässt sich aus dem
	// Rechnernamen bilden, und ein Pflichtfeld, das immer gleich
	// ausgefüllt wird, ist nur eine Hürde.
	var missing []string
	if s.To == "" {
		missing = append(missing, "to")
	}
	if s.Server == "" {
		missing = append(missing, "server")
	}

	switch len(missing) {
	case 0, 2:
		// Beides gesetzt oder beides leer — im zweiten Fall bleibt die
		// Meldung im Protokoll, und das ist eine gültige Wahl.
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("mail ist nur halb eingerichtet, es fehlt: %s", strings.Join(missing, ", "))
}

// From ist die Absenderadresse. Fehlt sie, wird sie aus dem Rechnernamen
// gebildet — so steht im Posteingang, von welchem Node die Meldung kam.
func (s Settings) from(host string) string {
	if s.From != "" {
		return s.From
	}
	return "pve-optimizer@" + host
}

// NewSender liefert den passenden Versandweg — oder einen, der nur
// protokolliert, wenn keiner eingerichtet ist. host geht in die
// Absenderadresse ein, falls keine eingetragen ist.
func NewSender(settings Settings, host string) Sender {
	if settings.To == "" || settings.Server == "" {
		return LogOnly{}
	}
	return &smtpSender{settings: settings, from: settings.from(host), now: time.Now}
}

// LogOnly ist der Versandweg, wenn keiner eingerichtet ist: Die Meldung
// bleibt im Protokoll. Das ist eine gültige Wahl — der Dienst-Monitor
// startet Dienste auch ohne Mailserver wieder neu.
type LogOnly struct{}

func (LogOnly) Send(string, string) error { return nil }
func (LogOnly) Describe() string          { return "nur im protokoll (kein mailserver eingerichtet)" }

type smtpSender struct {
	settings Settings
	from     string
	now      func() time.Time
}

func (s *smtpSender) Describe() string {
	return fmt.Sprintf("mail an %s über %s", s.settings.To, s.settings.Server)
}

func (s *smtpSender) Send(subject, body string) error {
	if err := smtp.SendMail(s.settings.Server, s.auth(), s.from, []string{s.settings.To}, s.compose(subject, body)); err != nil {
		return fmt.Errorf("mail an %s über %s: %w", s.settings.To, s.settings.Server, err)
	}
	return nil
}

// auth bleibt leer, solange kein Benutzer eingetragen ist. net/smtp gibt
// ein Passwort nur über eine verschlüsselte Verbindung heraus; ein Relay
// im eigenen Netz nimmt üblicherweise ohne Anmeldung an.
func (s *smtpSender) auth() smtp.Auth {
	if s.settings.Username == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(s.settings.Server)
	if err != nil {
		host = s.settings.Server
	}
	return smtp.PlainAuth("", s.settings.Username, s.settings.Password, host)
}

func (s *smtpSender) compose(subject, body string) []byte {
	var message strings.Builder
	fmt.Fprintf(&message, "From: %s\r\n", s.from)
	fmt.Fprintf(&message, "To: %s\r\n", s.settings.To)
	// Umlaute gehören in einer Betreffzeile kodiert, sonst zeigen manche
	// Programme Buchstabensalat.
	fmt.Fprintf(&message, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&message, "Date: %s\r\n", s.now().Format(time.RFC1123Z))
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	message.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(message.String())
}
