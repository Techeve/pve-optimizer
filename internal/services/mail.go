package services

import (
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"sort"
	"strings"
	"time"
)

// Mail ist der Weg, auf dem die Meldung den Node verlässt.
//
// Bewusst ein eigener SMTP-Zugang statt des sendmail des Nodes: Ein
// frisch aufgesetzter Proxmox-Node hat zwar ein postfix, aber keinen
// Relay und als Empfängeradresse eine Platzhalteradresse. Eine Mail über
// diesen Weg landet in der Warteschlange und nie bei einem Menschen — und
// eine Warnung, die niemand bekommt, ist schlimmer als keine.
type Mail struct {
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
func (m Mail) Check() error {
	set := map[string]string{"to": m.To, "from": m.From, "server": m.Server}

	var missing []string
	var given bool
	for key, value := range set {
		if value == "" {
			missing = append(missing, key)
			continue
		}
		given = true
	}

	if !given || len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("mail ist nur halb eingerichtet, es fehlt: %s", strings.Join(missing, ", "))
}

// Sender liefert den passenden Versandweg — oder einen, der nur
// protokolliert, wenn keiner eingerichtet ist.
func (m Mail) Sender() Sender {
	if m.To == "" || m.Server == "" {
		return logOnly{}
	}
	return &smtpSender{mail: m, now: time.Now}
}

type logOnly struct{}

func (logOnly) Send(string, string) error { return nil }
func (logOnly) Describe() string          { return "nur im protokoll (kein mailserver eingerichtet)" }

type smtpSender struct {
	mail Mail
	now  func() time.Time
}

func (s *smtpSender) Describe() string {
	return fmt.Sprintf("mail an %s über %s", s.mail.To, s.mail.Server)
}

func (s *smtpSender) Send(subject, body string) error {
	if err := smtp.SendMail(s.mail.Server, s.auth(), s.mail.From, []string{s.mail.To}, s.compose(subject, body)); err != nil {
		return fmt.Errorf("mail an %s über %s: %w", s.mail.To, s.mail.Server, err)
	}
	return nil
}

// auth bleibt leer, solange kein Benutzer eingetragen ist. net/smtp gibt
// ein Passwort nur über eine verschlüsselte Verbindung heraus; ein Relay
// im eigenen Netz nimmt üblicherweise ohne Anmeldung an.
func (s *smtpSender) auth() smtp.Auth {
	if s.mail.Username == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(s.mail.Server)
	if err != nil {
		host = s.mail.Server
	}
	return smtp.PlainAuth("", s.mail.Username, s.mail.Password, host)
}

func (s *smtpSender) compose(subject, body string) []byte {
	var message strings.Builder
	fmt.Fprintf(&message, "From: %s\r\n", s.mail.From)
	fmt.Fprintf(&message, "To: %s\r\n", s.mail.To)
	// Umlaute gehören in einer Betreffzeile kodiert, sonst zeigen manche
	// Programme Buchstabensalat.
	fmt.Fprintf(&message, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&message, "Date: %s\r\n", s.now().Format(time.RFC1123Z))
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	message.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(message.String())
}
