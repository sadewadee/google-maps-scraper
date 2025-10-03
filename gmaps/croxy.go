package gmaps

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/scrapemate"
	"github.com/playwright-community/playwright-go"
)

// CroxySiteJob fetches a business website HTML through CroxyProxy (browser-based)
// and applies the same enrichment (emails, socials, meta, tracking) as EmailExtractJob.
// It is intended as a fallback for non-fast mode and non-Google domains only.
//
// Best practices:
// - Keep Croxy usage strictly for site enrichment (not Maps).
// - Use short, reasonable timeouts and minimal retries.
// - Reuse existing enrichment helpers to keep code DRY and maintainable.
type CroxySiteJob struct {
	scrapemate.Job

	Entry          *Entry
	Croxy          CroxyConfig
	TargetURL      string
	ExitMonitor    exiter.Exiter
	usageInResults bool
}

// NewCroxySiteJob constructs a CroxySiteJob for fallback site fetching via CroxyProxy.
// parentID is the PlaceJob ID to keep lineage; entry holds website URL and will be enriched.
// croxy contains CroxyProxy endpoint and timeout configuration.
func NewCroxySiteJob(parentID string, entry *Entry, croxy CroxyConfig, opts ...func(*CroxySiteJob)) *CroxySiteJob {
	const (
		defaultPrio       = scrapemate.PriorityHigh
		defaultMaxRetries = 0
	)

	job := CroxySiteJob{
		Job: scrapemate.Job{
			ID:         uuid.New().String(),
			ParentID:   parentID,
			Method:     http.MethodGet,
			URL:        entry.WebSite, // logical target (not the Croxy URL)
			MaxRetries: defaultMaxRetries,
			Priority:   defaultPrio,
		},
		Entry:          entry,
		Croxy:          croxy,
		usageInResults: true,
	}

	for _, opt := range opts {
		opt(&job)
	}

	return &job
}

// WithCroxyJobExitMonitor attaches an ExitMonitor to the job (optional).
func WithCroxyJobExitMonitor(exitMonitor exiter.Exiter) func(*CroxySiteJob) {
	return func(j *CroxySiteJob) {
		j.ExitMonitor = exitMonitor
	}
}

// WithCroxyTargetURL overrides the target URL to be loaded via CroxyProxy (used for candidate pages).
func WithCroxyTargetURL(u string) func(*CroxySiteJob) {
	return func(j *CroxySiteJob) {
		j.TargetURL = strings.TrimSpace(u)
	}
}

// UseInResults indicates this job's output should be written to results.
func (j *CroxySiteJob) UseInResults() bool {
	return j.usageInResults
}

// Process parses the HTML (resp.Body) and enriches the Entry with emails, socials, meta, tracking.
// This reuses the same helpers used by EmailExtractJob to keep code clean and consistent.
func (j *CroxySiteJob) Process(ctx context.Context, resp *scrapemate.Response) (any, []scrapemate.IJob, error) {
	defer func() {
		resp.Document = nil
		resp.Body = nil
	}()

	if j.ExitMonitor != nil {
		defer j.ExitMonitor.IncrPlacesCompleted(1)
		defer j.ExitMonitor.IncrCroxyUses(1)
	}

	// If browser fetch failed just return current entry
	if resp.Error != nil || len(resp.Body) == 0 {
		if j.ExitMonitor != nil {
			j.ExitMonitor.IncrCroxyFail(1)
		}
		return j.Entry, nil, nil
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(resp.Body)))
	if err != nil {
		return j.Entry, nil, nil
	}

	// Emails
	emails := docEmailExtractor(doc)
	if len(emails) == 0 {
		emails = regexEmailExtractor(resp.Body)
	}
	j.Entry.Emails = uniqueStrings(append(j.Entry.Emails, emails...))

	// Socials, meta, tracking (reuse helpers from emailjob.go)
	extendSocialFromDoc(doc, j.Entry)
	extendSocialFromJSONLD(doc, j.Entry)
	extractMetaFromDoc(doc, j.Entry)
	extractTrackingFromBody(resp.Body, j.Entry)

	if j.ExitMonitor != nil {
		j.ExitMonitor.IncrCroxySuccess(1)
	}

	return j.Entry, nil, nil
}

// BrowserActions drives CroxyProxy to render the target website and returns the HTML of the embedded target frame (not the Croxy wrapper).
// This job MUST NOT be used for Google Maps URLs. It is strictly for business websites.
func (j *CroxySiteJob) BrowserActions(ctx context.Context, page playwright.Page) scrapemate.Response {
	var resp scrapemate.Response

	// Resolve timeouts
	timeoutSec := j.Croxy.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 25
	}
	timeoutMs := float64(time.Duration(timeoutSec) * time.Second / time.Millisecond)

	// Navigate to CroxyProxy landing
	pgResp, err := page.Goto(j.Croxy.ProxyURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
	})
	if err != nil {
		resp.Error = err
		return resp
	}

	// Best-effort: wait for Croxy input box and submit target
	//nolint:staticcheck // compatible with current playwright-go API style used across repo
	el, _ := page.WaitForSelector("input#url", playwright.PageWaitForSelectorOptions{
		Timeout: playwright.Float(timeoutMs),
	})

	target := ensureHTTP(j.Entry.WebSite)
	if j.TargetURL != "" {
		target = ensureHTTP(j.TargetURL)
	}
	if el != nil && target != "" {
		_ = el.Fill(target) // ignore intermediate errors, fallback continues
		_ = page.Keyboard().Press("Enter")
	}

	// Wait shortly for Croxy to render iframe
	page.WaitForTimeout(timeoutMs / 10)

	// Try to locate the embedded target frame (heuristic: non-empty URL not pointing to croxy host)
	findTargetFrame := func() playwright.Frame {
		frames := page.Frames()
		for _, f := range frames {
			u := strings.ToLower(strings.TrimSpace(f.URL()))
			if u == "" || strings.HasPrefix(u, "about:") {
				continue
			}
			if strings.Contains(u, "croxyproxy") || strings.Contains(u, "croxy") {
				continue
			}
			return f
		}
		return nil
	}

	// Poll a few times up to timeout for the target frame to appear
	var targetFrame playwright.Frame
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for time.Now().Before(deadline) {
		targetFrame = findTargetFrame()
		if targetFrame != nil {
			break
		}
		page.WaitForTimeout(200) // 200ms backoff
	}

	var body string
	if targetFrame != nil {
		// Wait for body in the target frame (best-effort)
		_, _ = targetFrame.WaitForSelector("body", playwright.FrameWaitForSelectorOptions{
			Timeout: playwright.Float(timeoutMs / 2),
		})
		// Extract the actual site HTML from the target frame
		body, err = targetFrame.Content()
		if err == nil {
			resp.URL = targetFrame.URL()
		}
	}

	// Fallback to wrapper content if target frame missing or errored
	if body == "" || err != nil {
		body, err = page.Content()
		if err != nil {
			resp.Error = err
			return resp
		}
		// Use current page URL post-navigation (proxied target wrapper)
		resp.URL = page.URL()
	}

	// Fill response meta from initial Croxy page response
	resp.StatusCode = pgResp.Status()
	resp.Headers = make(http.Header, len(pgResp.Headers()))
	for k, v := range pgResp.Headers() {
		resp.Headers.Add(k, v)
	}
	resp.Body = []byte(body)

	return resp
}

// ensureHTTP adds a scheme if missing; CroxyProxy expects a full URL.
func ensureHTTP(u string) string {
	s := strings.TrimSpace(u)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}
	return "http://" + s
}
