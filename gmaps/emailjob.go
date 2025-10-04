package gmaps

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/scrapemate"
	"github.com/mcnijman/go-emailaddress"
)

type EmailExtractJobOptions func(*EmailExtractJob)

type CroxyConfig struct {
	Enabled    bool
	ProxyURL   string
	TimeoutSec int
}

type EmailExtractJob struct {
	scrapemate.Job

	Entry          *Entry
	ExitMonitor    exiter.Exiter
	Croxy          CroxyConfig
	usageInResults bool
}

func NewEmailJob(parentID string, entry *Entry, opts ...EmailExtractJobOptions) *EmailExtractJob {
	const (
		defaultPrio       = scrapemate.PriorityHigh
		defaultMaxRetries = 0
	)

	job := EmailExtractJob{
		Job: scrapemate.Job{
			ID:         uuid.New().String(),
			ParentID:   parentID,
			Method:     "GET",
			URL:        entry.WebSite,
			MaxRetries: defaultMaxRetries,
			Priority:   defaultPrio,
		},
		// Defaults: Croxy enabled for non-fast path (Email jobs are only created in non-fast flows)
		Croxy: CroxyConfig{
			Enabled:    true,
			ProxyURL:   "https://www.croxyproxy.com/",
			TimeoutSec: 25,
		},
		usageInResults: true,
	}

	job.Entry = entry

	for _, opt := range opts {
		opt(&job)
	}

	return &job
}

func WithEmailJobExitMonitor(exitMonitor exiter.Exiter) EmailExtractJobOptions {
	return func(j *EmailExtractJob) {
		j.ExitMonitor = exitMonitor
	}
}

func WithEmailJobCroxy(c CroxyConfig) EmailExtractJobOptions {
	return func(j *EmailExtractJob) {
		j.Croxy = c
	}
}

// UseInResults controls whether this job's Process output is written to results.
// When Croxy fallback is scheduled, EmailExtractJob returns nil and should not be written.
func (j *EmailExtractJob) UseInResults() bool {
	return j.usageInResults
}

func (j *EmailExtractJob) Process(ctx context.Context, resp *scrapemate.Response) (any, []scrapemate.IJob, error) {
	defer func() {
		resp.Document = nil
		resp.Body = nil
	}()

	log := scrapemate.GetLoggerFromContext(ctx)
	log.Info("Processing email job", "url", j.URL)
	var nextJobs []scrapemate.IJob

	// Decide Croxy fallback strictly for non-Google hosts when fetch error or empty body
	fallbackReason := ""
	if j.Croxy.Enabled {
		host := func() string {
			u, err := url.Parse(j.Entry.WebSite)
			if err != nil || u == nil {
				return ""
			}
			return strings.ToLower(u.Hostname())
		}()
		if host != "" && !strings.Contains(host, "google") {
			if resp.Error != nil {
				fallbackReason = "fetch_error"
			} else if len(resp.Body) == 0 {
				fallbackReason = "empty_body"
			}
		}
	}

	if fallbackReason != "" {
		// Schedule CroxySiteJob fallback for homepage only (do not attempt for candidate pages)
		opts := []func(*CroxySiteJob){}
		if j.ExitMonitor != nil {
			opts = append(opts, WithCroxyJobExitMonitor(j.ExitMonitor))
		}
		cj := NewCroxySiteJob(j.ID, j.Entry, j.Croxy, opts...)
		j.usageInResults = false
		log.Info("Scheduling Croxy fallback", "target", j.Entry.WebSite, "parent_job_id", j.ID, "reason", fallbackReason)
		return nil, []scrapemate.IJob{cj}, nil
	}

	// Normal enrichment path
	doc, ok := resp.Document.(*goquery.Document)
	if !ok {
		// No document and not scheduling croxy: just return current entry
		return j.Entry, nextJobs, nil
	}

	// 1) Emails from current page
	emails := docEmailExtractor(doc)
	if len(emails) == 0 {
		emails = regexEmailExtractor(resp.Body)
	}
	j.Entry.Emails = uniqueStrings(append(j.Entry.Emails, emails...))
	log.Info("Homepage emails extracted", "found", len(emails), "total", len(j.Entry.Emails), "url", j.URL)

	// 2) Social links (arrays + legacy single fields), meta, tracking from current page
	extendSocialFromDoc(doc, j.Entry)
	extendSocialFromJSONLD(doc, j.Entry)
	extractMetaFromDoc(doc, j.Entry)
	extractTrackingFromBody(resp.Body, j.Entry)
	// Populate legacy single social fields for CSV and extract phone
	socialMediaExtractor(doc, j.Entry)
	phoneExtractor(doc, j.Entry)
	// Backfill single social fields from arrays when only JSON-LD provided
	backfillLegacySocialFromArrays(j.Entry)

	// 3) Follow in-site candidate pages (Contact/About/Privacy) up to depth 3 (best-effort)
	const maxFollow = 3
	base := j.URL
	candidates := sameDomainCandidates(doc, base, maxFollow)

	client := &http.Client{Timeout: 8 * time.Second}
	for i := range candidates {
		select {
		case <-ctx.Done():
			break
		default:
		}

		link := candidates[i]
		body, d, err := fetchDoc(ctx, client, link)

		// Decide Croxy fallback for this candidate page if needed
		shouldCroxyForLink := false
		if j.Croxy.Enabled {
			uh, _ := url.Parse(link)
			host := ""
			if uh != nil {
				host = strings.ToLower(uh.Hostname())
			}
			// never use Croxy for Google domains
			if host != "" && !strings.Contains(host, "google") {
				if err != nil || d == nil || len(body) == 0 {
					shouldCroxyForLink = true
				}
			}
		}

		if shouldCroxyForLink {
			opts := []func(*CroxySiteJob){}
			if j.ExitMonitor != nil {
				opts = append(opts, WithCroxyJobExitMonitor(j.ExitMonitor))
			}
			// target this candidate URL specifically through Croxy
			opts = append(opts, WithCroxyTargetURL(link))
			log.Info("Scheduling Croxy fallback for candidate", "candidate_url", link, "parent_job_id", j.ID, "reason", "fetch_error_or_empty_body")
			nextJobs = append(nextJobs, NewCroxySiteJob(j.ID, j.Entry, j.Croxy, opts...))
			continue
		}

		if err != nil || d == nil {
			continue
		}

		// Enrich per page
		em := docEmailExtractor(d)
		if len(em) == 0 {
			em = regexEmailExtractor(body)
		}
		j.Entry.Emails = uniqueStrings(append(j.Entry.Emails, em...))
		log.Info("Candidate page emails extracted", "found", len(em), "total", len(j.Entry.Emails), "candidate_url", link)

		extendSocialFromDoc(d, j.Entry)
		extendSocialFromJSONLD(d, j.Entry)
		extractMetaFromDoc(d, j.Entry)
		extractTrackingFromBody(body, j.Entry)
		// Populate legacy social fields and phone from candidate page
		socialMediaExtractor(d, j.Entry)
		phoneExtractor(d, j.Entry)
		backfillLegacySocialFromArrays(j.Entry)
	}

	// Mark place completed in ExitMonitor only on successful enrichment path
	if j.ExitMonitor != nil {
		j.ExitMonitor.IncrPlacesCompleted(1)
	}

	return j.Entry, nil, nil
}

func (j *EmailExtractJob) ProcessOnFetchError() bool {
	return true
}

func docEmailExtractor(doc *goquery.Document) []string {
	seen := map[string]bool{}

	var emails []string

	doc.Find("a[href^='mailto:']").Each(func(_ int, s *goquery.Selection) {
		mailto, exists := s.Attr("href")
		if exists {
			value := strings.TrimPrefix(mailto, "mailto:")
			if email, err := getValidEmail(value); err == nil {
				if !seen[email] {
					emails = append(emails, email)
					seen[email] = true
				}
			}
		}
	})

	return emails
}

func socialMediaExtractor(doc *goquery.Document, entry *Entry) {
	// Backward compatibility: keep first occurrence in legacy fields
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if !exists {
			return
		}
		if entry.Facebook == "" && strings.Contains(href, "facebook.com") {
			entry.Facebook = href
		}
		if entry.Instagram == "" && strings.Contains(href, "instagram.com") {
			entry.Instagram = href
		}
		if entry.LinkedIn == "" && strings.Contains(href, "linkedin.com") {
			entry.LinkedIn = href
		}
		if entry.WhatsApp == "" && (strings.Contains(href, "whatsapp.com") || strings.Contains(href, "wa.me")) {
			entry.WhatsApp = href
		}
	})
}
func phoneExtractor(doc *goquery.Document, entry *Entry) {
	if doc == nil || entry == nil {
		return
	}
	var found []string

	// Extract tel: anchors
	doc.Find("a[href^='tel:']").Each(func(_ int, s *goquery.Selection) {
		href := strings.TrimSpace(s.AttrOr("href", ""))
		if href == "" {
			return
		}
		num := strings.TrimSpace(strings.TrimPrefix(href, "tel:"))
		// Basic cleanup of common encodings/formatting
		num = strings.ReplaceAll(num, "%20", " ")
		num = strings.ReplaceAll(num, "-", " ")
		num = strings.ReplaceAll(num, "(0)", "0")
		if num != "" {
			found = append(found, num)
		}
	})

	// Normalize and add to Entry.Phones
	for _, f := range found {
		nums := normalizePhones(f, entry.CompleteAddress.Country)
		addTo(&entry.Phones, nums...)
	}

	// Set primary Entry.Phone if missing
	if strings.TrimSpace(entry.Phone) == "" {
		for _, f := range found {
			if strings.TrimSpace(f) != "" {
				entry.Phone = f
				break
			}
		}
	}
}

func backfillLegacySocialFromArrays(entry *Entry) {
	if entry == nil {
		return
	}
	if entry.Facebook == "" && len(entry.FacebookLinks) > 0 {
		entry.Facebook = entry.FacebookLinks[0]
	}
	if entry.Instagram == "" && len(entry.InstagramLinks) > 0 {
		entry.Instagram = entry.InstagramLinks[0]
	}
	if entry.LinkedIn == "" && len(entry.LinkedInLinks) > 0 {
		entry.LinkedIn = entry.LinkedInLinks[0]
	}
	// WhatsApp does not have an array; it is set via anchor extraction when present
}

func regexEmailExtractor(body []byte) []string {
	seen := map[string]bool{}

	var emails []string

	// Deobfuscate email addresses
	sBody := string(body)

	// HTML entity decode (e.g., &#64; -> @, &#46; -> .)
	sBody = html.UnescapeString(sBody)

	// Common obfuscations for "at"
	sBody = strings.ReplaceAll(sBody, "[at]", "@")
	sBody = strings.ReplaceAll(sBody, "(at)", "@")
	sBody = strings.ReplaceAll(sBody, " at ", "@")

	// Common obfuscations for "dot"
	sBody = strings.ReplaceAll(sBody, "[dot]", ".")
	sBody = strings.ReplaceAll(sBody, "(dot)", ".")
	sBody = strings.ReplaceAll(sBody, " dot ", ".")
	sBody = strings.ReplaceAll(sBody, "[.]", ".")

	body = []byte(sBody)

	addresses := emailaddress.Find(body, false)
	for i := range addresses {
		addr := addresses[i].String()
		if !seen[addr] {
			emails = append(emails, addr)
			seen[addr] = true
		}
	}

	return emails
}

// ----------------- Enrichment helpers -----------------

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func addTo(arr *[]string, vals ...string) {
	if arr == nil {
		return
	}
	*arr = uniqueStrings(append(*arr, vals...))
}

func extendSocialFromDoc(doc *goquery.Document, entry *Entry) {
	if doc == nil || entry == nil {
		return
	}
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		href := strings.TrimSpace(s.AttrOr("href", ""))
		if href == "" {
			return
		}
		l := strings.ToLower(href)
		switch {
		case strings.Contains(l, "facebook.com"):
			addTo(&entry.FacebookLinks, href)
		case strings.Contains(l, "instagram.com"):
			addTo(&entry.InstagramLinks, href)
		case strings.Contains(l, "linkedin.com"):
			addTo(&entry.LinkedInLinks, href)
		case strings.Contains(l, "pinterest."):
			addTo(&entry.PinterestLinks, href)
		case strings.Contains(l, "tiktok.com"):
			addTo(&entry.TiktokLinks, href)
		case strings.Contains(l, "twitter.com") || strings.Contains(l, "x.com"):
			addTo(&entry.TwitterLinks, href)
		case strings.Contains(l, "yelp.com"):
			addTo(&entry.YelpLinks, href)
		case strings.Contains(l, "youtube.com") || strings.Contains(l, "youtu.be"):
			addTo(&entry.YoutubeLinks, href)
		}
	})
}

func extendSocialFromJSONLD(doc *goquery.Document, entry *Entry) {
	if doc == nil || entry == nil {
		return
	}
	doc.Find(`script[type="application/ld+json"]`).Each(func(_ int, s *goquery.Selection) {
		raw := strings.TrimSpace(s.Text())
		if raw == "" {
			return
		}
		var anyJSON any
		if err := json.Unmarshal([]byte(raw), &anyJSON); err != nil {
			return
		}
		extractSameAsLinks(anyJSON, entry)
	})
}

func extractSameAsLinks(node any, entry *Entry) {
	switch v := node.(type) {
	case map[string]any:
		for k, vv := range v {
			if strings.EqualFold(k, "sameAs") {
				switch arr := vv.(type) {
				case []any:
					for _, it := range arr {
						if s, ok := it.(string); ok {
							l := strings.ToLower(s)
							switch {
							case strings.Contains(l, "facebook.com"):
								addTo(&entry.FacebookLinks, s)
							case strings.Contains(l, "instagram.com"):
								addTo(&entry.InstagramLinks, s)
							case strings.Contains(l, "linkedin.com"):
								addTo(&entry.LinkedInLinks, s)
							case strings.Contains(l, "pinterest."):
								addTo(&entry.PinterestLinks, s)
							case strings.Contains(l, "tiktok.com"):
								addTo(&entry.TiktokLinks, s)
							case strings.Contains(l, "twitter.com") || strings.Contains(l, "x.com"):
								addTo(&entry.TwitterLinks, s)
							case strings.Contains(l, "yelp.com"):
								addTo(&entry.YelpLinks, s)
							case strings.Contains(l, "youtube.com") || strings.Contains(l, "youtu.be"):
								addTo(&entry.YoutubeLinks, s)
							}
						}
					}
				}
			} else {
				extractSameAsLinks(vv, entry)
			}
		}
	case []any:
		for _, it := range v {
			extractSameAsLinks(it, entry)
		}
	}
}

func extractMetaFromDoc(doc *goquery.Document, entry *Entry) {
	if entry == nil || doc == nil {
		return
	}
	if entry.Meta.Title == "" {
		entry.Meta.Title = strings.TrimSpace(doc.Find("head title").First().Text())
	}
	if entry.Meta.Description == "" {
		// Try meta name=description then og:description
		if v := strings.TrimSpace(doc.Find(`meta[name="description"]`).AttrOr("content", "")); v != "" {
			entry.Meta.Description = v
		} else if v := strings.TrimSpace(doc.Find(`meta[property="og:description"]`).AttrOr("content", "")); v != "" {
			entry.Meta.Description = v
		}
	}
}

var (
	reUA  = regexp.MustCompile(`UA-\d{4,}-\d+`)
	reGA4 = regexp.MustCompile(`G-[A-Z0-9]{6,}`)
)

func extractTrackingFromBody(body []byte, entry *Entry) {
	if entry == nil || len(body) == 0 {
		return
	}
	s := string(body)
	if entry.TrackingIDs.Google.UA == "" {
		if m := reUA.FindString(s); m != "" {
			entry.TrackingIDs.Google.UA = m
		}
	}
	if entry.TrackingIDs.Google.GA4 == "" {
		if m := reGA4.FindString(s); m != "" {
			entry.TrackingIDs.Google.GA4 = m
		}
	}
}

func fetchDoc(ctx context.Context, client *http.Client, u string) ([]byte, *goquery.Document, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return body, nil, err
	}
	return body, doc, nil
}

func sameDomainCandidates(doc *goquery.Document, base string, max int) []string {
	if doc == nil || base == "" || max <= 0 {
		return nil
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil
	}
	isCandidate := func(href, text string) bool {
		t := strings.ToLower(text + " " + href)
		return strings.Contains(t, "contact") ||
			strings.Contains(t, "about") ||
			strings.Contains(t, "privacy") ||
			strings.Contains(t, "kontak") ||
			strings.Contains(t, "tentang") ||
			strings.Contains(t, "hubungi")
	}
	var out []string
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		if len(out) >= max {
			return
		}
		href := strings.TrimSpace(s.AttrOr("href", ""))
		if href == "" {
			return
		}
		u, err := url.Parse(href)
		if err != nil {
			return
		}
		if !u.IsAbs() {
			u = baseURL.ResolveReference(u)
		}
		if strings.EqualFold(u.Hostname(), baseURL.Hostname()) && isCandidate(href, s.Text()) {
			out = append(out, u.String())
		}
	})
	return uniqueStrings(out)
}

func getValidEmail(s string) (string, error) {
	email, err := emailaddress.Parse(strings.TrimSpace(s))
	if err != nil {
		return "", err
	}

	return email.String(), nil
}

// containsBlockSignals attempts to detect content indicating blocking/captcha/verification.
func containsBlockSignals(s string) bool {
	if s == "" {
		return false
	}
	s = strings.ToLower(s)
	signals := []string{
		"captcha",
		"verify",
		"verification",
		"access denied",
		"blocked",
		"temporarily unavailable",
		"rate limit",
		"attention required",
		"just a moment",
		"please verify you are a human",
		"unusual traffic",
		"bot detection",
	}
	for _, sig := range signals {
		if strings.Contains(s, sig) {
			return true
		}
	}
	return false
}
