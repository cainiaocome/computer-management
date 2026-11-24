package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cainiaocome/computer-management/internal/protocol"
)

// Client talks to the fleet server. The agent only ever dials out.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient builds a client with sensible timeouts.
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (c *Client) post(ctx context.Context, path, secret string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "fleet-agent")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("%s: %s: %s", path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Enroll exchanges a one-time token for a device identity.
func (c *Client) Enroll(ctx context.Context, req protocol.EnrollRequest) (*protocol.EnrollResponse, error) {
	var out protocol.EnrollResponse
	if err := c.post(ctx, "/api/v1/enroll", "", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Checkin sends observed state and receives the desired policy bundle.
func (c *Client) Checkin(ctx context.Context, deviceID, secret string, req protocol.CheckinRequest) (*protocol.CheckinResponse, error) {
	var out protocol.CheckinResponse
	if err := c.post(ctx, "/api/v1/checkin", secret, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Report sends module outcomes for the last applied generation.
func (c *Client) Report(ctx context.Context, deviceID, secret string, generation int64, reports []protocol.ModuleReport) error {
	body := map[string]any{"generation": generation, "reports": reports}
	return c.post(ctx, "/api/v1/report", secret, body, nil)
}
