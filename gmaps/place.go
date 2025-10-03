package gmaps

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/gosom/scrapemate"
	"github.com/playwright-community/playwright-go"

	"github.com/gosom/google-maps-scraper/exiter"
)

type PlaceJobOptions func(*PlaceJob)

type PlaceJob struct {
	scrapemate.Job

	UsageInResultststs  bool
	ExtractEmail        bool
	ExitMonitor         exiter.Exiter
	ExtractExtraReviews bool
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

	// Build review URL with language (hl) and country (gl) parameters
	hl := ""
	if j.URLParams != nil {
		hl = j.URLParams["hl"]
	}
	gl := countryToRegion(entry.CompleteAddress.Country)
	if entry.PlaceID != "" {
		entry.ReviewURL = buildReviewURL(entry.PlaceID, hl, gl)
	}

	// Apply claimed inference from BrowserActions heuristic if present
	if v, ok := resp.Meta["claimed"].(string); ok && v != "" {
		entry.Claimed = v
	}

	if j.ExtractEmail && entry.IsWebsiteValidForEmail() {
		opts := []EmailExtractJobOptions{}
		if j.ExitMonitor != nil {
			opts = append(opts, WithEmailJobExitMonitor(j.ExitMonitor))
		}

		emailJob := NewEmailJob(j.ID, &entry, opts...)

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

	// Heuristic claimed detection from page content (language-agnostic best-effort)
	// If we see "Claim this business" or "Own this business", mark as NO.
	// If we see "verified" or "claimed" but not the prompt to claim, mark as YES.
	// Otherwise leave unset to be decided by other signals (e.g., Owner ID).
	content, _ := page.Content()
	claimed := ""
	lc := strings.ToLower(content)
	if strings.Contains(lc, "claim this business") || strings.Contains(lc, "own this business") {
		claimed = "NO"
	} else if strings.Contains(lc, "verified") || strings.Contains(lc, "claimed") {
		claimed = "YES"
	}
	resp.Meta["claimed"] = claimed

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
