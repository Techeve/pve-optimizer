package proxmox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// LocalClient ruft pvesh auf dem Node auf, auf dem der Dienst läuft. Das
// spart den API-Token, verlangt aber eine Installation je Node.
type LocalClient struct {
	binary string
}

func NewLocalClient() *LocalClient {
	return &LocalClient{binary: "pvesh"}
}

func (c *LocalClient) RecentTasks(ctx context.Context) ([]Task, error) {
	output, err := c.run(ctx, "get", "/cluster/tasks")
	if err != nil {
		return nil, fmt.Errorf("aufgaben abrufen: %w", err)
	}
	var tasks []Task
	if err := json.Unmarshal(output, &tasks); err != nil {
		return nil, fmt.Errorf("aufgaben auswerten: %w", err)
	}
	return tasks, nil
}

func (c *LocalClient) ListGuests(ctx context.Context) ([]Guest, error) {
	// Die Ressourcenart "vm" umfasst bei Proxmox beides, VMs und Container.
	output, err := c.run(ctx, "get", "/cluster/resources", "--type", "vm")
	if err != nil {
		return nil, fmt.Errorf("gastliste abrufen: %w", err)
	}
	var all []Guest
	if err := json.Unmarshal(output, &all); err != nil {
		return nil, fmt.Errorf("gastliste auswerten: %w", err)
	}
	return onlyKnownKinds(all), nil
}

func (c *LocalClient) GuestConfig(ctx context.Context, node string, kind Kind, vmid int) (map[string]string, error) {
	output, err := c.run(ctx, "get", guestPath(node, kind, vmid))
	if err != nil {
		return nil, fmt.Errorf("konfiguration von %s %d abrufen: %w", kind, vmid, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("konfiguration von %s %d auswerten: %w", kind, vmid, err)
	}
	return decodeConfig(raw), nil
}

func (c *LocalClient) UpdateGuestConfig(ctx context.Context, node string, kind Kind, vmid int, fields map[string]string) error {
	args := []string{"set", guestPath(node, kind, vmid)}
	for key, value := range fields {
		args = append(args, "-"+key, value)
	}
	if _, err := c.run(ctx, args...); err != nil {
		return fmt.Errorf("konfiguration von %s %d schreiben: %w", kind, vmid, err)
	}
	return nil
}

// run ruft pvesh auf. Die Ausgabe kommt als JSON, damit beide Betriebsarten
// dieselben Strukturen auswerten.
func (c *LocalClient) run(ctx context.Context, args ...string) ([]byte, error) {
	args = append(args, "--output-format", "json")

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.binary, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return nil, fmt.Errorf("pvesh %s: %w", strings.Join(args, " "), err)
		}
		return nil, fmt.Errorf("pvesh %s: %s", strings.Join(args, " "), message)
	}
	return stdout.Bytes(), nil
}

func (c *LocalClient) NotBackedUp(ctx context.Context) ([]Guest, error) {
	// Proxmox beantwortet die Frage selbst — mitsamt der Feinheiten von
	// "all", "pool" und "exclude" in den Sicherungsaufträgen. Das
	// nachzubauen hieße, die Logik bei jeder Proxmox-Fassung nachzuziehen.
	output, err := c.run(ctx, "get", "/cluster/backup-info/not-backed-up")
	if err != nil {
		return nil, fmt.Errorf("ungesicherte gäste abrufen: %w", err)
	}
	var guests []Guest
	if err := json.Unmarshal(output, &guests); err != nil {
		return nil, fmt.Errorf("ungesicherte gäste auswerten: %w", err)
	}
	return guests, nil
}

func (c *LocalClient) Options(ctx context.Context) (Options, error) {
	output, err := c.run(ctx, "get", "/cluster/options")
	if err != nil {
		return Options{}, fmt.Errorf("rechenzentrums-einstellungen abrufen: %w", err)
	}
	var options Options
	if err := json.Unmarshal(output, &options); err != nil {
		return Options{}, fmt.Errorf("rechenzentrums-einstellungen auswerten: %w", err)
	}
	return options, nil
}

func (c *LocalClient) ReplicationJobs(ctx context.Context) ([]ReplicationJob, error) {
	output, err := c.run(ctx, "get", "/cluster/replication")
	if err != nil {
		return nil, fmt.Errorf("replikationsaufträge abrufen: %w", err)
	}
	var jobs []ReplicationJob
	if err := json.Unmarshal(output, &jobs); err != nil {
		return nil, fmt.Errorf("replikationsaufträge auswerten: %w", err)
	}
	return jobs, nil
}

func (c *LocalClient) Storages(ctx context.Context) ([]Storage, error) {
	output, err := c.run(ctx, "get", "/storage")
	if err != nil {
		return nil, fmt.Errorf("speicher abrufen: %w", err)
	}
	var storages []Storage
	if err := json.Unmarshal(output, &storages); err != nil {
		return nil, fmt.Errorf("speicher auswerten: %w", err)
	}
	return storages, nil
}
