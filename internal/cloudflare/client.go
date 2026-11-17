// Package cloudflare is a small client for the parts of the Cloudflare v4 DNS
// API that the updater uses.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrZoneNotFound is returned by ZoneID when no zone matches the name.
var ErrZoneNotFound = errors.New("zone not found")

// APIError is returned when Cloudflare answers with success=false. Status is
// the HTTP status code. Messages holds the messages from its errors array.
type APIError struct {
	Status   int
	Messages []string
}

func (e *APIError) Error() string {
	if len(e.Messages) == 0 {
		return fmt.Sprintf("cloudflare API request failed (HTTP %d)", e.Status)
	}
	return strings.Join(e.Messages, "; ")
}

// Record is the subset of a DNS record that the updater reads.
type Record struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

// RecordSpec is the body for creating or replacing a DNS record.
type RecordSpec struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

// Client talks to one Cloudflare API endpoint with one API token.
type Client struct {
	base  string // API base URL without a trailing slash.
	token string // API token, sent as a Bearer credential.
	hc    *http.Client
}

// New returns a client for the API at base, authenticated with token. Requests
// time out after 15 seconds.
func New(base, token string) *Client {
	return &Client{
		base:  strings.TrimSuffix(base, "/"),
		token: token,
		hc:    &http.Client{Timeout: 15 * time.Second},
	}
}

// envelope is the wrapper Cloudflare puts around every response. Result is
// kept raw because its shape depends on the endpoint: an array for lists, an
// object for a single record.
type envelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

// do sends one request. A nil body sends no request body. If result is non-nil
// and the response has a result, it is decoded into result. Responses larger
// than 1 MiB are truncated.
func (c *Client) do(ctx context.Context, method, path string, body, result any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare request: %w", err)
	}
	defer resp.Body.Close()

	var env envelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&env); err != nil {
		return fmt.Errorf("invalid response from Cloudflare (HTTP %d): %w", resp.StatusCode, err)
	}

	if !env.Success {
		apiErr := &APIError{Status: resp.StatusCode}
		for _, e := range env.Errors {
			if e.Message != "" {
				apiErr.Messages = append(apiErr.Messages, e.Message)
			}
		}
		return apiErr
	}

	if result != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, result); err != nil {
			return fmt.Errorf("unexpected result from Cloudflare: %w", err)
		}
	}
	return nil
}

// ZoneID returns the ID of the zone called name. It returns ErrZoneNotFound if
// no zone matches.
func (c *Client) ZoneID(ctx context.Context, name string) (string, error) {
	var zones []struct {
		ID string `json:"id"`
	}
	path := "/zones?" + url.Values{"name": {name}}.Encode()
	if err := c.do(ctx, http.MethodGet, path, nil, &zones); err != nil {
		return "", err
	}
	if len(zones) == 0 {
		return "", ErrZoneNotFound
	}
	return zones[0].ID, nil
}

// FindRecords returns every record of type typ named name in zoneID. An
// empty result means none exist, which is not an error. Only the first page
// (100 records) is read; one name and type never has that many.
func (c *Client) FindRecords(ctx context.Context, zoneID, typ, name string) ([]Record, error) {
	var recs []Record
	q := url.Values{"type": {typ}, "name": {name}, "per_page": {"100"}}.Encode()
	if err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/dns_records?"+q, nil, &recs); err != nil {
		return nil, err
	}
	return recs, nil
}

// WriteRecord updates record recID when it is non-empty, and creates a new
// record otherwise. Updates use PATCH so fields not in RecordSpec, such as the
// record's comment and tags, are kept.
func (c *Client) WriteRecord(ctx context.Context, zoneID, recID string, spec RecordSpec) error {
	path := "/zones/" + url.PathEscape(zoneID) + "/dns_records"
	if recID != "" {
		return c.do(ctx, http.MethodPatch, path+"/"+url.PathEscape(recID), spec, nil)
	}
	return c.do(ctx, http.MethodPost, path, spec, nil)
}
