package gmaps

import (
	"context"
	"errors"
	"testing"

	"github.com/gosom/scrapemate"
)

func TestEmailJob_NoCroxyOnSuccessBody(t *testing.T) {
	entry := &Entry{WebSite: "https://example.com"}
	j := NewEmailJob("parent", entry)
	// Ensure Croxy is enabled; logic must still avoid scheduling when body is present and no error.
	j.Croxy.Enabled = true

	resp := &scrapemate.Response{
		Body: []byte("<html><body>ok</body></html>"),
	}

	_, next, err := j.Process(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(next) != 0 {
		t.Fatalf("expected no Croxy fallback, got %d next jobs", len(next))
	}
	if !j.UseInResults() {
		t.Fatalf("expected UseInResults to be true when Croxy is not scheduled")
	}
}

func TestEmailJob_CroxyOnFetchError(t *testing.T) {
	entry := &Entry{WebSite: "https://example.com"}
	j := NewEmailJob("parent", entry)
	j.Croxy.Enabled = true

	resp := &scrapemate.Response{
		Error: errors.New("network error"),
	}

	_, next, err := j.Process(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(next) != 1 {
		t.Fatalf("expected 1 Croxy fallback job, got %d", len(next))
	}
	if j.UseInResults() {
		t.Fatalf("expected UseInResults to be false when Croxy is scheduled")
	}
}

func TestEmailJob_CroxyOnEmptyBody(t *testing.T) {
	entry := &Entry{WebSite: "https://example.com"}
	j := NewEmailJob("parent", entry)
	j.Croxy.Enabled = true

	resp := &scrapemate.Response{
		Body: []byte{},
	}

	_, next, err := j.Process(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(next) != 1 {
		t.Fatalf("expected 1 Croxy fallback job for empty body, got %d", len(next))
	}
	if j.UseInResults() {
		t.Fatalf("expected UseInResults to be false when Croxy is scheduled")
	}
}

func TestEmailJob_NoCroxyOnWeakSignals(t *testing.T) {
	entry := &Entry{WebSite: "https://example.com"}
	j := NewEmailJob("parent", entry)
	j.Croxy.Enabled = true

	// Body contains previously weak signals like "cloudflare" (now pruned from triggers)
	resp := &scrapemate.Response{
		Body: []byte("<html>cloudflare</html>"),
	}

	_, next, err := j.Process(context.Background(), resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(next) != 0 {
		t.Fatalf("expected no Croxy fallback when body exists even with weak signals, got %d", len(next))
	}
}
