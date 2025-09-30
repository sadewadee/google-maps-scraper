package gmaps

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/gosom/scrapemate"
	"github.com/playwright-community/playwright-go"

	"github.com/sadewadee/google-maps-scraper/exiter"
)

type PlaceJobOptions func(*PlaceJob)

type PlaceJob struct {
	scrapemate.Job

	UsageInResultststs  bool
	ExtractEmail        bool
	ExitMonitor         exiter.Exiter
	ExtractExtraReviews bool
	PreflightEnabled    bool

	// Preflight config (performance-focused quick checks)
	PreflightDNSTimeoutMs  int
	PreflightTCPTimeoutMs  int
	PreflightHEADTimeoutMs int
	PreflightEnableHEAD    bool
}

func NewPlaceJob(parentID, langCode, u string, extractEmail, extraExtraReviews bool, opts ...PlaceJobOptions) *PlaceJob {
	const (
		defaultPrio       = scrapemate.PriorityMedium
		defaultMaxRetries = 3
	)

	job := PlaceJob{
		Job: scrapemate.Job{
			ID:         uuid.New().String(),
			ParentID:   parentID,
			Method:     "GET",
			URL:        u,
			URLParams:  map[string]string{"hl": langCode},
			MaxRetries: defaultMaxRetries,
			Priority:   defaultPrio,
		},
	}

	job.UsageInResultststs = true
	job.ExtractEmail = extractEmail
	job.ExtractExtraReviews = extraExtraReviews
	// Enable URL preflight by default for performance
	job.PreflightEnabled = true

	for _, opt := range opts {
		opt(&job)
	}

	return &job
}

func WithPlaceJobExitMonitor(exitMonitor exiter.Exiter) PlaceJobOptions {
	return func(j *PlaceJob) {
		j.ExitMonitor = exitMonitor
	}
}

// WithPlacePreflightEnabled toggles URL preflight checks before visiting websites for email extraction.
func WithPlacePreflightEnabled(enabled bool) PlaceJobOptions {
	return func(j *PlaceJob) {
		j.PreflightEnabled = enabled
	}
}

// WithPlacePreflightConfig sets preflight quick-check timeouts and HEAD enable flag.
func WithPlacePreflightConfig(dnsMs, tcpMs, headMs int, enableHead bool) PlaceJobOptions {
	return func(j *PlaceJob) {
		if dnsMs > 0 {
			j.PreflightDNSTimeoutMs = dnsMs
		}
		if tcpMs > 0 {
			j.PreflightTCPTimeoutMs = tcpMs
		}
		if headMs > 0 {
			j.PreflightHEADTimeoutMs = headMs
		}
		j.PreflightEnableHEAD = enableHead
	}
}

// UseInResults controls whether this job's Process output is written to results.
// When the PlaceJob redirects to an EmailExtractJob, it returns nil data and
// should not be written; we toggle the internal flag accordingly.
func (j *PlaceJob) UseInResults() bool {
	return j.UsageInResultststs
}

func (j *PlaceJob) Process(_ context.Context, resp *scrapemate.Response) (any, []scrapemate.IJob, error) {
	defer func() {
		resp.Document = nil
		resp.Body = nil
		resp.Meta = nil
	}()

	raw, ok := resp.Meta["json"].([]byte)
	if !ok {
		return nil, nil, fmt.Errorf("could not convert to []byte")
	}

	entry, err := EntryFromJSON(raw)
	if err != nil {
		return nil, nil, err
	}

	entry.ID = j.ParentID

	if entry.Link == "" {
		entry.Link = j.GetURL()
	}

	allReviewsRaw, ok := resp.Meta["reviews_raw"].(fetchReviewsResponse)
	if ok && len(allReviewsRaw.pages) > 0 {
		entry.AddExtraReviews(allReviewsRaw.pages)
	}

	if j.ExtractEmail && entry.IsWebsiteValidForEmail() {
		if j.PreflightEnabled {
			// Added the URL Preflight: run fast DNS/TCP (optional HEAD) checks before visiting the website
			// to avoid long timeouts on dead URLs. Chains to EmailExtractJob only when alive.
			opts := []EmailPreflightJobOptions{}
			if j.ExitMonitor != nil {
				opts = append(opts, WithEmailPreflightExitMonitor(j.ExitMonitor))
			}
			// Apply configured preflight timeouts and HEAD policy when provided
			opts = append(opts, WithEmailPreflightTimeouts(j.PreflightDNSTimeoutMs, j.PreflightTCPTimeoutMs, j.PreflightHEADTimeoutMs, j.PreflightEnableHEAD))

			preflightJob := NewEmailPreflightJob(j.ID, &entry, opts...)

			// Do not write this PlaceJob's output; final data will be produced downstream by the chained job.
			j.UsageInResultststs = false

			return nil, []scrapemate.IJob{preflightJob}, nil
		}

		// Preflight disabled: enqueue EmailExtractJob directly (legacy behavior).
		eopts := []EmailExtractJobOptions{}
		if j.ExitMonitor != nil {
			eopts = append(eopts, WithEmailJobExitMonitor(j.ExitMonitor))
		}
		emailJob := NewEmailJob(j.ID, &entry, eopts...)

		j.UsageInResultststs = false

		return nil, []scrapemate.IJob{emailJob}, nil
	} else if j.ExitMonitor != nil {
		j.ExitMonitor.IncrPlacesCompleted(1)
	}

	return &entry, nil, err
}

func (j *PlaceJob) BrowserActions(ctx context.Context, page playwright.Page) scrapemate.Response {
	var resp scrapemate.Response

	pageResponse, err := page.Goto(j.GetURL(), playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
	})
	if err != nil {
		resp.Error = err

		return resp
	}

	if err = clickRejectCookiesIfRequired(page); err != nil {
		resp.Error = err

		return resp
	}

	const defaultTimeout = 5000

	err = page.WaitForURL(page.URL(), playwright.PageWaitForURLOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(defaultTimeout),
	})
	if err != nil {
		resp.Error = err

		return resp
	}

	resp.URL = pageResponse.URL()
	resp.StatusCode = pageResponse.Status()
	resp.Headers = make(http.Header, len(pageResponse.Headers()))

	for k, v := range pageResponse.Headers() {
		resp.Headers.Add(k, v)
	}

	raw, err := j.extractJSON(page)
	if err != nil {
		resp.Error = err

		return resp
	}

	if resp.Meta == nil {
		resp.Meta = make(map[string]any)
	}

	resp.Meta["json"] = raw

	if j.ExtractExtraReviews {
		reviewCount := j.getReviewCount(raw)
		if reviewCount > 8 { // we have more reviews
			params := fetchReviewsParams{
				page:        page,
				mapURL:      page.URL(),
				reviewCount: reviewCount,
			}

			reviewFetcher := newReviewFetcher(params)

			reviewData, err := reviewFetcher.fetch(ctx)
			if err != nil {
				return resp
			}

			resp.Meta["reviews_raw"] = reviewData
		}
	}

	return resp
}

func (j *PlaceJob) extractJSON(page playwright.Page) ([]byte, error) {
	rawI, err := page.Evaluate(js)
	if err != nil {
		return nil, err
	}

	raw, ok := rawI.(string)
	if !ok {
		return nil, fmt.Errorf("could not convert to string")
	}
	const prefix = ")]}'"
	raw = strings.TrimSpace(strings.TrimPrefix(raw, prefix))
	return []byte(raw), nil
}

const js = `
function parse() {
	const appState = window.APP_INITIALIZATION_STATE[3];
	if (!appState) {
		return null;
	}
	const keys = Object.keys(appState);
	const key = keys[0];
	if (appState[key] && appState[key][6]) {
		return appState[key][6];
	}
	return null;
}
`

// getReviewCount extracts only the review count from the raw place JSON
func (j *PlaceJob) getReviewCount(raw []byte) int {
	entry, err := EntryFromJSON(raw, true)
	if err != nil {
		return 0
	}

	return entry.ReviewCount
}
