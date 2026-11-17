package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newServer returns a client wired to a fake API that always answers with body.
func newServer(t *testing.T, body string) *Client {
	t.Helper()
	return newStatusServer(t, http.StatusOK, body)
}

// newStatusServer is newServer with a chosen HTTP status code.
func newStatusServer(t *testing.T, status int, body string) *Client {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)
	return New(s.URL, "token")
}

func TestZoneID(t *testing.T) {
	c := newServer(t, `{"success":true,"result":[{"id":"z1"}]}`)
	id, err := c.ZoneID(context.Background(), "example.com")
	if err != nil || id != "z1" {
		t.Errorf("ZoneID = %q, %v; want z1, nil", id, err)
	}
}

func TestZoneIDNotFound(t *testing.T) {
	c := newServer(t, `{"success":true,"result":[]}`)
	_, err := c.ZoneID(context.Background(), "example.com")
	if !errors.Is(err, ErrZoneNotFound) {
		t.Errorf("err = %v, want ErrZoneNotFound", err)
	}
}

func TestAPIErrorCarriesMessages(t *testing.T) {
	c := newServer(t, `{"success":false,"errors":[{"message":"bad token"},{"message":"try later"}]}`)
	_, err := c.ZoneID(context.Background(), "example.com")

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if len(apiErr.Messages) != 2 || apiErr.Messages[0] != "bad token" {
		t.Errorf("messages = %v", apiErr.Messages)
	}
}

func TestFindRecordsNoneIsNotAnError(t *testing.T) {
	c := newServer(t, `{"success":true,"result":[]}`)
	recs, err := c.FindRecords(context.Background(), "z1", "A", "home.example.com")
	if err != nil || len(recs) != 0 {
		t.Errorf("recs=%v err=%v; want none, nil", recs, err)
	}
}

func TestFindRecordsReturnsAll(t *testing.T) {
	c := newServer(t, `{"success":true,"result":[{"id":"rec1","content":"203.0.113.7"},{"id":"rec2","content":"203.0.113.8"}]}`)
	recs, err := c.FindRecords(context.Background(), "z1", "A", "home.example.com")
	if err != nil || len(recs) != 2 || recs[0].ID != "rec1" || recs[1].Content != "203.0.113.8" {
		t.Errorf("recs=%+v err=%v", recs, err)
	}
}

// A create or update answers with the record as a JSON object. That must
// decode without error.
func TestWriteRecordAcceptsObjectResult(t *testing.T) {
	c := newServer(t, `{"success":true,"errors":[],"messages":[],"result":{"id":"rec9","content":"203.0.113.7"}}`)
	err := c.WriteRecord(context.Background(), "z1", "", RecordSpec{Type: "A", Name: "home.example.com", Content: "203.0.113.7", TTL: 1})
	if err != nil {
		t.Errorf("WriteRecord: %v", err)
	}
}

func TestInvalidJSONIsReported(t *testing.T) {
	c := newStatusServer(t, http.StatusBadGateway, `<html>gateway error</html>`)
	_, err := c.ZoneID(context.Background(), "example.com")
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("err = %v, want error naming HTTP 502", err)
	}
}

func TestAPIErrorWithoutMessagesNamesStatus(t *testing.T) {
	c := newStatusServer(t, http.StatusForbidden, `{"success":false,"errors":[]}`)
	_, err := c.ZoneID(context.Background(), "example.com")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("err = %v, want APIError with HTTP 403", err)
	}
}
