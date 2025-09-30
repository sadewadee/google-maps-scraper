package gmaps

import (
	"context"
	"strings"
	"regexp"

	"github.com/PuerkitoBio/goquery"
	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/scrapemate"
	"github.com/mcnijman/go-emailaddress"
)

type EmailExtractJobOptions func(*EmailExtractJob)

type EmailExtractJob struct {
	scrapemate.Job

	Entry       *Entry
	ExitMonitor exiter.Exiter
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

	defer func() {
		if j.ExitMonitor != nil {
			j.ExitMonitor.IncrPlacesCompleted(1)
		}
	}()

	log := scrapemate.GetLoggerFromContext(ctx)

	log.Info("Processing email job", "url", j.URL)

	// if html fetch failed just return
	if resp.Error != nil {
		return j.Entry, nil, nil
	}

	doc, ok := resp.Document.(*goquery.Document)
	if !ok {
		return j.Entry, nil, nil
	}

	emails := docEmailExtractor(doc)
	if len(emails) == 0 {
		emails = regexEmailExtractor(resp.Body)
	}

	// Extract emails with (at), [at], and at variations
	atVariationEmails := extractEmailsWithAtVariations(resp.Body)
	emails = append(emails, atVariationEmails...)

	// Remove duplicates
	emails = removeDuplicateEmails(emails)

	j.Entry.Emails = emails

	// Extract social media links from website if not already found in Google Maps
	j.extractSocialMediaFromWebsite(doc, resp.Body)

	return j.Entry, nil, nil
}

// extractSocialMediaFromWebsite extracts social media links from business website
func (j *EmailExtractJob) extractSocialMediaFromWebsite(doc *goquery.Document, body []byte) {
	// Extract from HTML links
	j.extractSocialMediaFromHTML(doc)
	
	// Extract from page content using regex
	j.extractSocialMediaFromContent(string(body))
}

// extractSocialMediaFromHTML extracts social media links from HTML anchor tags
func (j *EmailExtractJob) extractSocialMediaFromHTML(doc *goquery.Document) {
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if !exists {
			return
		}
		
		href = strings.ToLower(href)
		
		// Facebook
		if (strings.Contains(href, "facebook.com") || strings.Contains(href, "fb.com")) && j.Entry.Facebook == "" {
			j.Entry.Facebook = normalizeURL(href)
		}
		
		// Instagram
		if (strings.Contains(href, "instagram.com") || strings.Contains(href, "instagr.am")) && j.Entry.Instagram == "" {
			j.Entry.Instagram = normalizeURL(href)
		}
		
		// LinkedIn
		if strings.Contains(href, "linkedin.com") && j.Entry.LinkedIn == "" {
			j.Entry.LinkedIn = normalizeURL(href)
		}
		
		// Twitter/X
		if (strings.Contains(href, "twitter.com") || strings.Contains(href, "x.com")) && j.Entry.Twitter == "" {
			j.Entry.Twitter = normalizeURL(href)
		}
	})
}

// extractSocialMediaFromContent extracts social media links from page content using regex
func (j *EmailExtractJob) extractSocialMediaFromContent(content string) {
	// Facebook regex patterns
	if j.Entry.Facebook == "" {
		fbRegex := regexp.MustCompile(`(?i)https?://(?:www\.)?(?:facebook\.com|fb\.com)/[a-zA-Z0-9._-]+`)
		if matches := fbRegex.FindStringSubmatch(content); len(matches) > 0 {
			j.Entry.Facebook = normalizeURL(matches[0])
		}
	}
	
	// Instagram regex patterns
	if j.Entry.Instagram == "" {
		igRegex := regexp.MustCompile(`(?i)https?://(?:www\.)?(?:instagram\.com|instagr\.am)/[a-zA-Z0-9._-]+`)
		if matches := igRegex.FindStringSubmatch(content); len(matches) > 0 {
			j.Entry.Instagram = normalizeURL(matches[0])
		}
	}
	
	// LinkedIn regex patterns
	if j.Entry.LinkedIn == "" {
		liRegex := regexp.MustCompile(`(?i)https?://(?:www\.)?linkedin\.com/(?:in|company)/[a-zA-Z0-9._-]+`)
		if matches := liRegex.FindStringSubmatch(content); len(matches) > 0 {
			j.Entry.LinkedIn = normalizeURL(matches[0])
		}
	}
	
	// Twitter/X regex patterns
	if j.Entry.Twitter == "" {
		twRegex := regexp.MustCompile(`(?i)https?://(?:www\.)?(?:twitter\.com|x\.com)/[a-zA-Z0-9._-]+`)
		if matches := twRegex.FindStringSubmatch(content); len(matches) > 0 {
			j.Entry.Twitter = normalizeURL(matches[0])
		}
	}
}

// normalizeURL normalizes and validates social media URLs
func normalizeURL(url string) string {
	url = strings.TrimSpace(url)
	
	// Remove common URL parameters and fragments
	if idx := strings.Index(url, "?"); idx != -1 {
		url = url[:idx]
	}
	if idx := strings.Index(url, "#"); idx != -1 {
		url = url[:idx]
	}
	
	// Ensure HTTPS
	if strings.HasPrefix(url, "http://") {
		url = "https://" + url[7:]
	} else if !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}
	
	// Remove trailing slash
	url = strings.TrimSuffix(url, "/")
	
	return url
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

func regexEmailExtractor(body []byte) []string {
	seen := map[string]bool{}

	var emails []string

	addresses := emailaddress.Find(body, false)
	for i := range addresses {
		if !seen[addresses[i].String()] {
			emails = append(emails, addresses[i].String())
			seen[addresses[i].String()] = true
		}
	}

	return emails
}

// extractEmailsWithAtVariations extracts emails with (at), [at], and at variations
func extractEmailsWithAtVariations(body []byte) []string {
	seen := map[string]bool{}
	var emails []string

	// Regex patterns for different at variations
	patterns := []string{
		`\b[a-zA-Z0-9._%+-]+\(at\)[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}\b`,  // info(at)domain.com
		`\b[a-zA-Z0-9._%+-]+\[at\][a-zA-Z0-9.-]+\.[a-zA-Z]{2,}\b`,  // info[at]domain.com
		`\b[a-zA-Z0-9._%+-]+\s+at\s+[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}\b`, // info at domain.com
	}

	bodyStr := string(body)

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindAllString(bodyStr, -1)

		for _, match := range matches {
			// Convert variations to standard email format
			normalizedEmail := normalizeAtVariations(match)
			
			// Validate the normalized email
			if email, err := getValidEmail(normalizedEmail); err == nil {
				if !seen[email] {
					emails = append(emails, email)
					seen[email] = true
				}
			}
		}
	}

	return emails
}

// normalizeAtVariations converts (at), [at], and at variations to @ symbol
func normalizeAtVariations(email string) string {
	// Replace (at) with @
	email = regexp.MustCompile(`\(at\)`).ReplaceAllString(email, "@")
	
	// Replace [at] with @
	email = regexp.MustCompile(`\[at\]`).ReplaceAllString(email, "@")
	
	// Replace " at " with @
	email = regexp.MustCompile(`\s+at\s+`).ReplaceAllString(email, "@")
	
	return email
}

// removeDuplicateEmails removes duplicate emails from slice
func removeDuplicateEmails(emails []string) []string {
	seen := map[string]bool{}
	var result []string

	for _, email := range emails {
		if !seen[email] {
			result = append(result, email)
			seen[email] = true
		}
	}

	return result
}

func getValidEmail(s string) (string, error) {
	email, err := emailaddress.Parse(strings.TrimSpace(s))
	if err != nil {
		return "", err
	}

	return email.String(), nil
}
