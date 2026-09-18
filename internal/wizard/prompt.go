package wizard

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"pve-optimizer/internal/mode"
)

// prompt stellt die Fragen. Jede hat eine Vorgabe, die eine leere Eingabe
// übernimmt — wer nichts weiß, drückt Enter und bekommt etwas
// Vernünftiges.
type prompt struct {
	in  *bufio.Scanner
	out io.Writer
	// eof merkt sich, dass keine Eingabe mehr kommt. Ohne das würde eine
	// Wiederholung nach ungültiger Eingabe endlos drehen, sobald der
	// Wizard ohne Eingabekanal läuft.
	eof bool
}

func newPrompt(in io.Reader, out io.Writer) *prompt {
	return &prompt{in: bufio.NewScanner(in), out: out}
}

func (p *prompt) say(format string, args ...any) {
	fmt.Fprintf(p.out, format+"\n", args...)
}

// ask stellt eine Frage und liefert die Antwort, oder die Vorgabe.
func (p *prompt) ask(question, vorgabe string) string {
	if vorgabe == "" {
		fmt.Fprintf(p.out, "%s: ", question)
	} else {
		fmt.Fprintf(p.out, "%s [%s]: ", question, vorgabe)
	}

	if p.eof || !p.in.Scan() {
		p.eof = true
		fmt.Fprintln(p.out)
		return vorgabe
	}
	if answer := strings.TrimSpace(p.in.Text()); answer != "" {
		return answer
	}
	return vorgabe
}

// retry wiederholt eine Frage, bis die Antwort taugt — oder bis keine
// Eingabe mehr kommt.
func (p *prompt) retry(question, vorgabe string, parse func(string) error) {
	for {
		answer := p.ask(question, vorgabe)
		err := parse(answer)
		if err == nil {
			return
		}
		p.say("  %v", err)
		if p.eof {
			// Ohne Eingabekanal lässt sich nichts klären. Die Vorgabe ist
			// geprüft, also gilt sie.
			_ = parse(vorgabe)
			return
		}
	}
}

func (p *prompt) yesNo(question string, vorgabe bool) bool {
	hint := "j/N"
	if vorgabe {
		hint = "J/n"
	}

	var result = vorgabe
	p.retry(question+" ("+hint+")", "", func(answer string) error {
		switch strings.ToLower(answer) {
		case "":
			result = vorgabe
		case "j", "ja", "y", "yes":
			result = true
		case "n", "nein", "no":
			result = false
		default:
			return fmt.Errorf("bitte j oder n")
		}
		return nil
	})
	return result
}

func (p *prompt) number(question string, vorgabe int) int {
	result := vorgabe
	p.retry(question, strconv.Itoa(vorgabe), func(answer string) error {
		value, err := strconv.Atoi(answer)
		if err != nil || value < 0 {
			return fmt.Errorf("bitte eine ganze zahl ab 0")
		}
		result = value
		return nil
	})
	return result
}

func (p *prompt) mode(question string, vorgabe mode.Mode, erlaubt ...mode.Mode) mode.Mode {
	if len(erlaubt) == 0 {
		erlaubt = []mode.Mode{mode.Off, mode.Report, mode.Enforce}
	}
	namen := make([]string, 0, len(erlaubt))
	for _, m := range erlaubt {
		namen = append(namen, string(m))
	}

	result := vorgabe
	p.retry(question+" ("+strings.Join(namen, "/")+")", string(vorgabe), func(answer string) error {
		for _, m := range erlaubt {
			if string(m) == answer {
				result = m
				return nil
			}
		}
		return fmt.Errorf("bitte %s", strings.Join(namen, ", "))
	})
	return result
}

func (p *prompt) duration(question string, vorgabe time.Duration) time.Duration {
	result := vorgabe
	p.retry(question, short(vorgabe), func(answer string) error {
		if answer == "0" {
			result = 0
			return nil
		}
		value, err := time.ParseDuration(answer)
		if err != nil || value < 0 {
			return fmt.Errorf("bitte eine zeitspanne wie \"30s\" oder \"168h\"")
		}
		result = value
		return nil
	})
	return result
}

// vmids liest eine Liste von Gastnummern, durch Komma oder Leerzeichen
// getrennt.
func (p *prompt) vmids(question string, vorgabe []int) []int {
	result := vorgabe
	p.retry(question, joinVMIDs(vorgabe), func(answer string) error {
		if answer == "" || answer == "-" {
			result = nil
			return nil
		}
		var parsed []int
		for _, field := range strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || r == ' ' }) {
			value, err := strconv.Atoi(strings.TrimSpace(field))
			if err != nil {
				return fmt.Errorf("%q ist keine gastnummer", field)
			}
			parsed = append(parsed, value)
		}
		sort.Ints(parsed)
		result = parsed
		return nil
	})
	return result
}

func joinVMIDs(vmids []int) string {
	parts := make([]string, 0, len(vmids))
	for _, vmid := range vmids {
		parts = append(parts, strconv.Itoa(vmid))
	}
	return strings.Join(parts, ", ")
}
