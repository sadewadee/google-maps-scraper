package gmaps

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/google/uuid"
	"github.com/gosom/scrapemate"
	"github.com/playwright-community/playwright-go"
	"github.com/sadewadee/google-maps-scraper/deduper"
	"github.com/sadewadee/google-maps-scraper/exiter"
)

type GmapJobOptions func(*GmapJob)

type GmapJob struct {
	scrapemate.Job

	MaxDepth         int
	LangCode         string
	ExtractEmail     bool
	PreflightEnabled bool

	// Preflight config (propagated to PlaceJob)
	PreflightDNSTimeoutMs  int
	PreflightTCPTimeoutMs  int
	PreflightHEADTimeoutMs int
	PreflightEnableHEAD    bool

	Deduper             deduper.Deduper
	ExitMonitor         exiter.Exiter
	ExtractExtraReviews bool
}

func NewGmapJob(
	id, langCode, query string,
	maxDepth int,
	extractEmail bool,
	geoCoordinates string,
	zoom int,
	opts ...GmapJobOptions,
) *GmapJob {
	query = url.QueryEscape(query)

	const (
		maxRetries = 3
		prio       = scrapemate.PriorityLow
	)

	if id == "" {
		id = uuid.New().String()
	}

	mapURL := ""
	if geoCoordinates != "" && zoom > 0 {
		mapURL = fmt.Sprintf("https://www.google.com/maps/search/%s/@%s,%dz", query, strings.ReplaceAll(geoCoordinates, " ", ""), zoom)
	} else {
		//Warning: geo and zoom MUST be both set or not
		mapURL = fmt.Sprintf("https://www.google.com/maps/search/%s", query)
	}

	job := GmapJob{
		Job: scrapemate.Job{
			ID:         id,
			Method:     http.MethodGet,
			URL:        mapURL,
			URLParams:  map[string]string{"hl": langCode},
			MaxRetries: maxRetries,
			Priority:   prio,
		},
		MaxDepth:         maxDepth,
		LangCode:         langCode,
		ExtractEmail:     extractEmail,
		PreflightEnabled: true, // enable URL preflight by default
	}

	for _, opt := range opts {
		opt(&job)
	}

	return &job
}

func WithDeduper(d deduper.Deduper) GmapJobOptions {
	return func(j *GmapJob) {
		j.Deduper = d
	}
}

func WithExitMonitor(e exiter.Exiter) GmapJobOptions {
	return func(j *GmapJob) {
		j.ExitMonitor = e
	}
}

func WithExtraReviews() GmapJobOptions {
	return func(j *GmapJob) {
		j.ExtractExtraReviews = true
	}
}

// WithPreflightEnabled toggles URL preflight checks before visiting websites for email extraction.
func WithPreflightEnabled(enabled bool) GmapJobOptions {
	return func(j *GmapJob) {
		j.PreflightEnabled = enabled
	}
}

// WithPreflightConfig sets preflight quick-check timeouts and HEAD enable flag.
func WithPreflightConfig(dnsMs, tcpMs, headMs int, enableHead bool) GmapJobOptions {
	return func(j *GmapJob) {
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

func (j *GmapJob) UseInResults() bool {
	return false
}

func (j *GmapJob) Process(ctx context.Context, resp *scrapemate.Response) (any, []scrapemate.IJob, error) {
	defer func() {
		resp.Document = nil
		resp.Body = nil
	}()

	log := scrapemate.GetLoggerFromContext(ctx)

	doc, ok := resp.Document.(*goquery.Document)
	if !ok {
		return nil, nil, fmt.Errorf("could not convert to goquery document")
	}

	var next []scrapemate.IJob

	if strings.Contains(resp.URL, "/maps/place/") {
		jopts := []PlaceJobOptions{}
		if j.ExitMonitor != nil {
			jopts = append(jopts, WithPlaceJobExitMonitor(j.ExitMonitor))
		}

		// propagate preflight flag and config to PlaceJob
		popts := append(jopts,
			WithPlacePreflightEnabled(j.PreflightEnabled),
			WithPlacePreflightConfig(j.PreflightDNSTimeoutMs, j.PreflightTCPTimeoutMs, j.PreflightHEADTimeoutMs, j.PreflightEnableHEAD),
		)
		placeJob := NewPlaceJob(j.ID, j.LangCode, resp.URL, j.ExtractEmail, j.ExtractExtraReviews, popts...)

		next = append(next, placeJob)
	} else {
		doc.Find(`div[role=feed] div[jsaction]>a`).Each(func(_ int, s *goquery.Selection) {
			if href := s.AttrOr("href", ""); href != "" {
				jopts := []PlaceJobOptions{}
				if j.ExitMonitor != nil {
					jopts = append(jopts, WithPlaceJobExitMonitor(j.ExitMonitor))
				}

				// propagate preflight flag and config to PlaceJob
				popts := append(jopts,
					WithPlacePreflightEnabled(j.PreflightEnabled),
					WithPlacePreflightConfig(j.PreflightDNSTimeoutMs, j.PreflightTCPTimeoutMs, j.PreflightHEADTimeoutMs, j.PreflightEnableHEAD),
				)
				nextJob := NewPlaceJob(j.ID, j.LangCode, href, j.ExtractEmail, j.ExtractExtraReviews, popts...)

				if j.Deduper == nil || j.Deduper.AddIfNotExists(ctx, href) {
					next = append(next, nextJob)
				}
			}
		})
	}

	if j.ExitMonitor != nil {
		j.ExitMonitor.IncrPlacesFound(len(next))
		j.ExitMonitor.IncrSeedCompleted(1)
	}

	log.Info(fmt.Sprintf("%d places found", len(next)))

	return nil, next, nil
}

func (j *GmapJob) BrowserActions(ctx context.Context, page playwright.Page) scrapemate.Response {
	var resp scrapemate.Response

	pageResponse, err := page.Goto(j.GetFullURL(), playwright.PageGotoOptions{
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

	// When Google Maps finds only 1 place, it slowly redirects to that place's URL
	// check element scroll
	sel := `div[role='feed']`

	//nolint:staticcheck // TODO replace with the new playwright API
	_, err = page.WaitForSelector(sel, playwright.PageWaitForSelectorOptions{
		Timeout: playwright.Float(700),
	})

	var singlePlace bool

	if err != nil {
		waitCtx, waitCancel := context.WithTimeout(ctx, time.Second*5)
		defer waitCancel()

		singlePlace = waitUntilURLContains(waitCtx, page, "/maps/place/")

		waitCancel()
	}

	if singlePlace {
		resp.URL = page.URL()

		var body string

		body, err = page.Content()
		if err != nil {
			resp.Error = err
			return resp
		}

		resp.Body = []byte(body)

		return resp
	}

	scrollSelector := `div[role='feed']`

	_, err = scroll(ctx, page, j.MaxDepth, scrollSelector)
	if err != nil {
		resp.Error = err

		return resp
	}

	body, err := page.Content()
	if err != nil {
		resp.Error = err
		return resp
	}

	resp.Body = []byte(body)

	return resp
}

func waitUntilURLContains(ctx context.Context, page playwright.Page, s string) bool {
	ticker := time.NewTicker(time.Millisecond * 150)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if strings.Contains(page.URL(), s) {
				return true
			}
		}
	}
}

func clickRejectCookiesIfRequired(page playwright.Page) error {
	// click the cookie reject button if exists
	sel := `form[action="https://consent.google.com/save"]:first-of-type button:first-of-type`

	const timeout = 500

	//nolint:staticcheck // TODO replace with the new playwright API
	el, err := page.WaitForSelector(sel, playwright.PageWaitForSelectorOptions{
		Timeout: playwright.Float(timeout),
	})

	if err != nil {
		return nil
	}

	if el == nil {
		return nil
	}

	//nolint:staticcheck // TODO replace with the new playwright API
	return el.Click()
}

func scroll(ctx context.Context,
	page playwright.Page,
	maxDepth int,
	scrollSelector string,
) (int, error) {
	// Resilient multi-selector scrolling with null-safe evaluation and viewport fallback
	log := scrapemate.GetLoggerFromContext(ctx)

	candidates := []string{
		scrollSelector,
		"div[role='region']",
		"div[aria-label='Results']",
		"div[jscontroller][role='feed']",
	}

	// Build JSON array of selectors for inlined JS
	var b strings.Builder
	b.WriteString("[")
	for i, s := range candidates {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(fmt.Sprintf("%q", s))
	}
	b.WriteString("]")
	selectorsJSON := b.String()

	var currentHeight int64
	consecutiveNoChange := 0
	attempts := 0

	const (
		baseDelayMs = 300
		maxDelayMs  = 2000
		maxNoChange = 3
	)

	for i := 0; i < maxDepth; i++ {
		attempts++
		delay := baseDelayMs * attempts
		if delay > maxDelayMs {
			delay = maxDelayMs
		}

		js := fmt.Sprintf(`async () => {
			const selectors = %s;
			let el = null, used = null;
			for (const s of selectors) {
				el = document.querySelector(s);
				if (el) { used = s; break; }
			}
			if (!el) {
				// Viewport-level fallback
				window.scrollBy(0, window.innerHeight);
				await new Promise(r => setTimeout(r, %d));
				return { used: null, height: document.documentElement.scrollHeight, viewport: true };
			}
			// Container scroll with null safety guaranteed
			el.scrollTop = el.scrollHeight;
			await new Promise(r => setTimeout(r, %d));
			return { used: used, height: el.scrollHeight, viewport: false };
		}`, selectorsJSON, delay, delay)

		res, err := page.Evaluate(js)
		if err != nil {
			if log != nil {
				log.Info(fmt.Sprintf("scroll_evaluate_error attempt=%d err=%v", attempts, err))
			}
			//nolint:staticcheck // TODO replace with the new playwright API
			page.WaitForTimeout(float64(delay))
			// If repeated errors, bubble up
			if attempts >= 2 {
				return attempts, err
			}
			continue
		}

		// Parse result: { used: string|null, height: number, viewport: boolean }
		usedSelector := ""
		viewport := false
		var height int64

		if m, ok := res.(map[string]any); ok {
			if u, ok2 := m["used"].(string); ok2 {
				usedSelector = u
			}
			if vp, ok2 := m["viewport"].(bool); ok2 {
				viewport = vp
			}
			switch h := m["height"].(type) {
			case float64:
				height = int64(h)
			case int:
				height = int64(h)
			case int64:
				height = h
			default:
				if hs, ok2 := m["height"].(string); ok2 {
					if hv, convErr := strconv.ParseInt(hs, 10, 64); convErr == nil {
						height = hv
					}
				}
			}
		} else if hv, ok := res.(int); ok {
			height = int64(hv)
		} else if hf, ok := res.(float64); ok {
			height = int64(hf)
		}

		if log != nil {
			log.Info(fmt.Sprintf("scroll_attempt attempt=%d selector=%q viewport=%v height=%d", attempts, usedSelector, viewport, height))
		}

		// End-of-scroll detection by consecutive no-change
		if height <= 0 {
			consecutiveNoChange++
		} else if height == currentHeight {
			consecutiveNoChange++
		} else {
			consecutiveNoChange = 0
			currentHeight = height
		}

		if consecutiveNoChange >= maxNoChange {
			if log != nil {
				log.Info(fmt.Sprintf("scroll_stop reason=no_change attempts=%d height=%d", attempts, currentHeight))
			}
			break
		}

		select {
		case <-ctx.Done():
			if log != nil {
				log.Info("scroll_stop reason=ctx_done")
			}
			return int(currentHeight), nil
		default:
		}

		//nolint:staticcheck // TODO replace with the new playwright API
		page.WaitForTimeout(float64(delay))
	}

	return attempts, nil
}
