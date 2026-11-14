// Package render is the API tier's client for calcside-render, the
// stateless Vega-Lite → PNG service (EP-0024). It re-validates every
// spec against internal/chart before sending — the renderer is trusted
// but the API never forwards bytes it has not itself accepted.
package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"calcside/internal/chart"
)

const (
	// maxResponseBytes caps a single PNG reply; 8 MiB is far above any
	// spec the chart validator admits at scale 2 / max_side 1024.
	maxResponseBytes = 8 << 20
	clientTimeout    = 10 * time.Second
)

var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// Client POSTs validated chart specs to the renderer and returns PNG
// bytes. It is safe for concurrent use.
type Client struct {
	base string
	key  string
	hc   *http.Client
}

// New builds a client for baseURL (e.g. http://render:8091) using the
// shared bearer key.
func New(baseURL, key string) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		key:  key,
		hc:   &http.Client{Timeout: clientTimeout},
	}
}

type requestBody struct {
	Spec    json.RawMessage `json:"spec"`
	Scale   float64         `json:"scale"`
	MaxSide int             `json:"max_side"`
}

// Render validates spec and returns the rendered PNG. The API asks for
// scale 2 capped at 1024px on the longest side — sharp enough for MCP
// image content without oversized payloads.
func (c *Client) Render(ctx context.Context, spec []byte) ([]byte, error) {
	if c == nil {
		return nil, errors.New("render: no client")
	}
	canonical, err := chart.Validate(spec)
	if err != nil {
		return nil, fmt.Errorf("render: invalid spec: %w", err)
	}
	body, err := json.Marshal(requestBody{Spec: canonical, Scale: 2, MaxSide: 1024})
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/render", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("render: status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		return nil, fmt.Errorf("render: unexpected content type %q", ct)
	}
	png, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	if len(png) > maxResponseBytes {
		return nil, errors.New("render: response too large")
	}
	if !bytes.HasPrefix(png, pngMagic) {
		return nil, errors.New("render: response is not a PNG")
	}
	return png, nil
}
