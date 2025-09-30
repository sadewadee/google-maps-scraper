package gmaps

import (
	"context"
	"net/http"
	"strings"
	"time"

	emailverifier "github.com/AfterShip/email-verifier"
	"github.com/google/uuid"
	"github.com/gosom/scrapemate"
	"github.com/mcnijman/go-emailaddress"
	"github.com/playwright-community/playwright-go"

	"github.com/sadewadee/google-maps-scraper/exiter"
)

// EmailVerifyJobOptions configures EmailVerifyJob.
type EmailVerifyJobOptions func(*EmailVerifyJob)

// EmailVerifyJob performs fast email verification focused on performance.
// Policy: BIG-data optimized — DNS/MX-only check on the first extracted email with a 3s timeout.
// Outcome: Entry.Verified = true when at least one valid MX record exists for the email's domain.
type EmailVerifyJob struct {
	scrapemate.Job

	Entry       *Entry
	ExitMonitor exiter.Exiter
}

// NewEmailVerifyJob constructs a compute-only job (no HTTP fetch) to verify emails on an Entry.
func NewEmailVerifyJob(parentID string, entry *Entry, opts ...EmailVerifyJobOptions) *EmailVerifyJob {
	const (
		defaultPrio       = scrapemate.PriorityMedium
		defaultMaxRetries = 0
	)

	job := EmailVerifyJob{
		Job: scrapemate.Job{
			ID:         uuid.New().String(),
			ParentID:   parentID,
			Method:     "LOCAL",       // compute-only, no network fetch via scrapemate
			URL:        "about:blank", // valid no-op URL to avoid Playwright invalid URL navigation
			MaxRetries: defaultMaxRetries,
			Priority:   defaultPrio,
		},
		Entry: entry,
	}

	for _, opt := range opts {
		opt(&job)
	}

	return &job
}

// WithEmailVerifyJobExitMonitor sets the exit monitor for progress accounting.
func WithEmailVerifyJobExitMonitor(exitMonitor exiter.Exiter) EmailVerifyJobOptions {
	return func(j *EmailVerifyJob) {
		j.ExitMonitor = exitMonitor
	}
}

// Process uses AfterShip/email-verifier for fast-but-reliable validation on the first extracted email.
// Policy: BIG-data optimized — validate only the first email with a short timeout budget.
func (j *EmailVerifyJob) Process(ctx context.Context, _ *scrapemate.Response) (any, []scrapemate.IJob, error) {
	defer func() {
		if j.ExitMonitor != nil {
			j.ExitMonitor.IncrPlacesCompleted(1)
		}
	}()

	// default false
	j.Entry.Verified = false

	// no emails to verify
	if len(j.Entry.Emails) == 0 {
		return j.Entry, nil, nil
	}

	// fast-path: verify only the first email
	raw := strings.TrimSpace(j.Entry.Emails[0])
	if raw == "" {
		return j.Entry, nil, nil
	}

	// quick syntax parse to avoid unnecessary verifier calls
	if _, err := emailaddress.Parse(raw); err != nil {
		return j.Entry, nil, nil
	}

	// short timeout context for verification
	vctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// build a verifier once (no global state kept to keep this job simple)
	v := emailverifier.NewVerifier()

	// Execute verification within our timeout
	type outMsg struct{ deliverable bool }
	ch := make(chan outMsg, 1)
	go func() {
		defer func() { _ = recover() }()
		res, err := v.Verify(raw)
		if err != nil || res == nil {
			ch <- outMsg{deliverable: false}
			return
		}
		// Conservative pass condition: prefer SMTP deliverability if available; otherwise syntax+MX.
		// Fields provided by AfterShip Result.
		deliverable := false
		if res.SMTP != nil && res.SMTP.Deliverable {
			deliverable = true
		} else {
			if res.Syntax.Valid && res.HasMxRecords {
				deliverable = true
			}
		}
		// Additionally, consider Reachable == "yes" as a positive signal.
		if res.Reachable == "yes" {
			deliverable = true
		}
		ch <- outMsg{deliverable: deliverable}
	}()

	select {
	case <-vctx.Done():
		// timeout -> keep false (fail-closed for performance)
		return j.Entry, nil, nil
	case out := <-ch:
		if out.deliverable {
			j.Entry.Verified = true
		}
		return j.Entry, nil, nil
	}
}

// ProcessOnFetchError allows the job to proceed even if the pipeline marks fetch errors upstream.
func (j *EmailVerifyJob) ProcessOnFetchError() bool {
	return true
}

// UseInResults ensures the final verified Entry is written to results.
func (j *EmailVerifyJob) UseInResults() bool {
	return true
}

// BrowserActions is a no-op for compute-only LOCAL jobs to avoid jshttp calling the embedded default.
// It returns a minimal successful Response without using the browser page.
func (j *EmailVerifyJob) BrowserActions(_ context.Context, _ playwright.Page) scrapemate.Response {
	var resp scrapemate.Response
	resp.URL = j.GetURL()
	resp.StatusCode = http.StatusOK
	resp.Headers = make(http.Header)
	return resp
}
