package scaler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Client is the scaler's only line to pstack: HTTP with the root token.
type Client struct {
	Base, Token string
	HTTP        *http.Client
}

// Do sends body as JSON (when non-nil) and decodes a 2xx answer into out. A *string out takes the
// raw body (GET /api/swarm/join answers text/plain, W-R6). Non-2xx → the API's `error` text.
func (c Client) Do(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.Base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	switch o := out.(type) {
	case nil:
		return nil
	case *string:
		*o = string(b)
		return nil
	default:
		return json.Unmarshal(b, out)
	}
}
