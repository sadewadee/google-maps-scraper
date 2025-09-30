package gmaps

import (
	"context"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/google/uuid"
	"github.com/gosom/scrapemate"
	"github.com/mcnijman/go-emailaddress"
	"github.com/sadewadee/google-maps-scraper/exiter"
)

type EmailExtractJobOptions func(*EmailExtractJob)

type EmailExtractJob struct {
	scrapemate.Job

	Entry        *Entry
	ExitMonitor  exiter.Exiter
	useInResults bool
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
	}

	job.Entry = entry
	job.useInResults = true

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

func (j *EmailExtractJob) Process(ctx context.Context, resp *scrapemate.Response) (any, []scrapemate.IJob, error) {
	defer func() {
		resp.Document = nil
		resp.Body = nil
	}()

	log := scrapemate.GetLoggerFromContext(ctx)
	log.Info("Processing email job", "url", j.URL)

	// Helper to mark completion when we produce final Entry here (no verify step).
	markCompleted := func() {
		if j.ExitMonitor != nil {
			j.ExitMonitor.IncrPlacesCompleted(1)
		}
	}

	// If HTML fetch failed, return current entry as-is and mark completed.
	if resp.Error != nil {
		markCompleted()
		return j.Entry, nil, nil
	}

	doc, ok := resp.Document.(*goquery.Document)
	if !ok {
		markCompleted()
		return j.Entry, nil, nil
	}

	emails := docEmailExtractor(doc)
	if len(emails) == 0 {
		emails = regexEmailExtractor(resp.Body)
	}
	j.Entry.Emails = emails

	// Extract social links regardless
	socialMediaExtractor(doc, j.Entry)

	// If we found at least one email, enqueue a verification job (Flow B) and do NOT mark completed here.
	if len(j.Entry.Emails) > 0 {
		opts := []EmailVerifyJobOptions{}
		if j.ExitMonitor != nil {
			opts = append(opts, WithEmailVerifyJobExitMonitor(j.ExitMonitor))
		}
		verifyJob := NewEmailVerifyJob(j.ID, j.Entry, opts...)
		// Prevent writer from attempting to write this job (data is intentionally nil here).
		j.useInResults = false
		return nil, []scrapemate.IJob{verifyJob}, nil
	}

	// No emails found: finalize entry now and mark completed.
	markCompleted()
	return j.Entry, nil, nil
}

func (j *EmailExtractJob) ProcessOnFetchError() bool {
	return true
}

// UseInResults controls whether this job's output should be written by result writers.
// When a verification job is enqueued, this returns false to avoid emitting nil data.
func (j *EmailExtractJob) UseInResults() bool {
	return j.useInResults
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

func regexEmailExtractor(body []byte) []string {
	seen := map[string]bool{}

	var emails []string

	// Deobfuscate email addresses
	sBody := string(body)
	sBody = strings.ReplaceAll(sBody, "[at]", "@")
	sBody = strings.ReplaceAll(sBody, "(at)", "@")
	sBody = strings.ReplaceAll(sBody, " at ", "@")
	body = []byte(sBody)

	addresses := emailaddress.Find(body, false)
	for i := range addresses {
		if !seen[addresses[i].String()] {
			emails = append(emails, addresses[i].String())
			seen[addresses[i].String()] = true
		}
	}

	return emails
}

func getValidEmail(s string) (string, error) {
	email, err := emailaddress.Parse(strings.TrimSpace(s))
	if err != nil {
		return "", err
	}

	return email.String(), nil
}
