package gmaps

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gosom/scrapemate"
	"github.com/playwright-community/playwright-go"
)

const (
	croxyProxyURL        = "https://www.croxyproxy.com/"
	urlInputSelector     = "input#url"
	submitButtonSelector = "#requestSubmit"
	maxRetries           = 3
	defaultTimeout       = 30000
	navigationTimeout    = 60000
	loadTimeout          = 120000
)

var (
	userAgents = []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/116.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/116.0.0.0 Safari/537.36",
	}
	
	// Simple in-memory cache with expiration
	cache      = make(map[string]cacheEntry)
	cacheMutex sync.RWMutex
)

type cacheEntry struct {
	content   string
	expiresAt time.Time
}

type CroxyProxyJob struct {
	scrapemate.Job
	TargetURL string
}

func NewCroxyProxyJob(id, targetURL string) *CroxyProxyJob {
	if id == "" {
		id = fmt.Sprintf("croxy-%d", time.Now().UnixNano())
	}

	return &CroxyProxyJob{
		Job: scrapemate.Job{
			ID:         id,
			Method:     http.MethodGet,
			URL:        croxyProxyURL,
			MaxRetries: maxRetries,
			Priority:   scrapemate.PriorityHigh,
		},
		TargetURL: targetURL,
	}
}

func (j *CroxyProxyJob) UseInResults() bool {
	return true
}

func (j *CroxyProxyJob) Process(ctx context.Context, resp *scrapemate.Response) (any, []scrapemate.IJob, error) {
	// Return the HTML content as result
	if resp.Body != nil {
		return map[string]interface{}{
			"url":     j.TargetURL,
			"content": string(resp.Body),
			"status":  "success",
		}, nil, nil
	}
	
	return map[string]interface{}{
		"url":    j.TargetURL,
		"status": "failed",
		"error":  "no content retrieved",
	}, nil, nil
}

func (j *CroxyProxyJob) BrowserActions(ctx context.Context, page playwright.Page) scrapemate.Response {
	log := scrapemate.GetLoggerFromContext(ctx)
	
	// Check cache first
	if cached := getCachedContent(j.TargetURL); cached != "" {
		log.Info("Using cached CroxyProxy content")
		return scrapemate.Response{
			URL:        j.TargetURL,
			StatusCode: 200,
			Body:       []byte(cached),
		}
	}

	var resp scrapemate.Response
	
	for attempt := 1; attempt <= maxRetries; attempt++ {
		log.Info(fmt.Sprintf("CroxyProxy attempt %d/%d for %s", attempt, maxRetries, j.TargetURL))
		
		if err := j.performCroxyProxyRequest(ctx, page, log); err != nil {
			log.Error(fmt.Sprintf("Attempt %d failed: %v", attempt, err))
			if attempt == maxRetries {
				resp.Error = fmt.Errorf("failed to scrape after %d attempts: %v", maxRetries, err)
				return resp
			}
			continue
		}
		
		// Success - get content and cache it
		content, err := page.Content()
		if err != nil {
			log.Error(fmt.Sprintf("Failed to get page content: %v", err))
			continue
		}
		
		// Cache the content for 1 hour
		setCachedContent(j.TargetURL, content)
		
		resp.URL = j.TargetURL
		resp.StatusCode = 200
		resp.Body = []byte(content)
		
		log.Info("CroxyProxy request successful")
		return resp
	}
	
	resp.Error = fmt.Errorf("failed to retrieve content after all retries")
	return resp
}

func (j *CroxyProxyJob) performCroxyProxyRequest(ctx context.Context, page playwright.Page, log scrapemate.Logger) error {
	// Set random user agent
	userAgent := userAgents[rand.Intn(len(userAgents))]
	if err := page.SetUserAgent(userAgent); err != nil {
		return fmt.Errorf("failed to set user agent: %w", err)
	}
	
	// Set extra headers
	if err := page.SetExtraHTTPHeaders(map[string]string{
		"Accept-Language": "en-US,en;q=0.9",
	}); err != nil {
		return fmt.Errorf("failed to set headers: %w", err)
	}
	
	// Navigate to CroxyProxy
	_, err := page.Goto(croxyProxyURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(navigationTimeout),
	})
	if err != nil {
		return fmt.Errorf("failed to navigate to CroxyProxy: %w", err)
	}
	
	// Wait for URL input field
	if err := page.WaitForSelector(urlInputSelector, playwright.PageWaitForSelectorOptions{
		Timeout: playwright.Float(defaultTimeout),
	}); err != nil {
		return fmt.Errorf("URL input field not found: %w", err)
	}
	
	// Clear and type target URL
	if err := page.Fill(urlInputSelector, j.TargetURL); err != nil {
		return fmt.Errorf("failed to fill URL input: %w", err)
	}
	
	// Submit form and wait for navigation
	log.Info("Submitting form and waiting for navigation...")
	
	// Click submit button and wait for navigation
	if err := page.Click(submitButtonSelector); err != nil {
		return fmt.Errorf("failed to click submit button: %w", err)
	}
	
	// Wait for navigation
	if err := page.WaitForLoadState(playwright.LoadStateDomcontentloaded, playwright.PageWaitForLoadStateOptions{
		Timeout: playwright.Float(navigationTimeout),
	}); err != nil {
		return fmt.Errorf("navigation timeout: %w", err)
	}
	
	currentURL := page.URL()
	
	// Check for error conditions
	if strings.Contains(currentURL, "/requests?fso=") {
		return fmt.Errorf("error URL detected: %s", currentURL)
	}
	
	content, err := page.Content()
	if err != nil {
		return fmt.Errorf("failed to get page content for error check: %w", err)
	}
	
	contentLower := strings.ToLower(content)
	if strings.Contains(contentLower, "your session has outdated") || 
	   strings.Contains(contentLower, "something went wrong") {
		return fmt.Errorf("error text detected in page content")
	}
	
	// Handle proxy launching page
	if strings.Contains(contentLower, "proxy is launching") {
		log.Info("Proxy launching page detected. Waiting for final redirect...")
		if err := page.WaitForLoadState(playwright.LoadStateLoad, playwright.PageWaitForLoadStateOptions{
			Timeout: playwright.Float(loadTimeout),
		}); err != nil {
			return fmt.Errorf("final redirect timeout: %w", err)
		}
		log.Info(fmt.Sprintf("Redirected successfully to: %s", page.URL()))
	} else {
		log.Info(fmt.Sprintf("Mapped directly to: %s", page.URL()))
	}
	
	// Wait for CroxyProxy frame to render
	log.Info("Waiting for CroxyProxy frame to render...")
	if err := page.WaitForSelector("#__cpsHeaderTab", playwright.PageWaitForSelectorOptions{
		Timeout: playwright.Float(defaultTimeout),
	}); err != nil {
		return fmt.Errorf("CroxyProxy frame not found: %w", err)
	}
	
	log.Info("CroxyProxy frame rendered successfully")
	return nil
}

// Simple cache functions
func getCachedContent(url string) string {
	cacheMutex.RLock()
	defer cacheMutex.RUnlock()
	
	entry, exists := cache[url]
	if !exists || time.Now().After(entry.expiresAt) {
		return ""
	}
	
	return entry.content
}

func setCachedContent(url, content string) {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()
	
	cache[url] = cacheEntry{
		content:   content,
		expiresAt: time.Now().Add(time.Hour), // 1 hour expiration
	}
	
	// Simple cleanup - remove expired entries
	for k, v := range cache {
		if time.Now().After(v.expiresAt) {
			delete(cache, k)
		}
	}
}