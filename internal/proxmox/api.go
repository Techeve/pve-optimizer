package proxmox

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIClient spricht über HTTPS mit der Proxmox-Cluster-API und sieht damit
// alle Nodes auf einmal.
type APIClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewAPIClient erzeugt den Client. tokenID hat die Form "user@realm!name".
func NewAPIClient(baseURL, tokenID, tokenSecret string, insecureSkipVerify bool) *APIClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if insecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // bewusst konfigurierbar für selbstsignierte Proxmox-Zertifikate
	}

	return &APIClient{
		baseURL: strings.TrimSuffix(baseURL, "/") + "/api2/json",
		token:   fmt.Sprintf("PVEAPIToken=%s=%s", tokenID, tokenSecret),
		http:    &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}
}

func (c *APIClient) RecentTasks(ctx context.Context) ([]Task, error) {
	var tasks []Task
	if err := c.request(ctx, http.MethodGet, "/cluster/tasks", nil, &tasks); err != nil {
		return nil, fmt.Errorf("aufgaben abrufen: %w", err)
	}
	return tasks, nil
}

func (c *APIClient) VMConfig(ctx context.Context, node string, vmid int) (map[string]string, error) {
	var raw map[string]json.RawMessage
	if err := c.request(ctx, http.MethodGet, vmPath(node, vmid), nil, &raw); err != nil {
		return nil, fmt.Errorf("konfiguration von vm %d abrufen: %w", vmid, err)
	}
	return decodeConfig(raw), nil
}

func (c *APIClient) UpdateVMConfig(ctx context.Context, node string, vmid int, fields map[string]string) error {
	form := url.Values{}
	for key, value := range fields {
		form.Set(key, value)
	}
	if err := c.request(ctx, http.MethodPut, vmPath(node, vmid), strings.NewReader(form.Encode()), nil); err != nil {
		return fmt.Errorf("konfiguration von vm %d schreiben: %w", vmid, err)
	}
	return nil
}

// request führt den Aufruf aus und entpackt das "data"-Feld der Antwort.
// Ist out nil, wird der Rumpf verworfen.
func (c *APIClient) request(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("anfrage bauen: %w", err)
	}
	req.Header.Set("Authorization", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("anfrage senden: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("antwort lesen: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("proxmox antwortete mit %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		return nil
	}

	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("antwort auswerten: %w", err)
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("nutzdaten auswerten: %w", err)
	}
	return nil
}
