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
