// Package client provides the shared versioned API transport used by CLI and agent.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/koboshchan/kotatsu/internal/api"
	"github.com/koboshchan/kotatsu/internal/config"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(base, token string, dev bool) (*Client, error) {
	if err := config.ValidateURL(base, dev); err != nil {
		return nil, err
	}
	return &Client{BaseURL: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("API request failed (%T)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e api.Error
		_ = json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&e)
		return fmt.Errorf("API %d: %s", resp.StatusCode, e.Error)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}
