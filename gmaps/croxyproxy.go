package gmaps

import (
    "context"
    "fmt"
    "net/http"
    "net/url"
    "net"
    "strings"
    "sync"
    "time"

	"github.com/PuerkitoBio/goquery"
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
	TargetURL      string
	UsageInResults bool
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
		// Default to false; we only write results for place pages
		UsageInResults: false,
	}
}

func (j *CroxyProxyJob) UseInResults() bool {
	return j.UsageInResults
}

func (j *CroxyProxyJob) Process(ctx context.Context, resp *scrapemate.Response) (any, []scrapemate.IJob, error) {
    // If place JSON was extracted in BrowserActions, return a structured Entry
    if resp.Meta != nil {
        // If BrowserActions collected place links from Croxy frames, spawn sub-jobs
        if rawLinks, ok := resp.Meta["place_links"].([]string); ok && len(rawLinks) > 0 {
            var next []scrapemate.IJob

            for _, href := range rawLinks {
                h := strings.TrimSpace(href)
                if h == "" {
                    continue
                }

                lower := strings.ToLower(h)

                // unwrap croxy consent/share wrapper
                if strings.Contains(lower, "/m?continue=") {
                    if u, err := url.Parse(h); err == nil {
                        cont := u.Query().Get("continue")
                        if cont != "" {
                            if contDecoded, err2 := url.QueryUnescape(cont); err2 == nil {
                                h = contDecoded
                                lower = strings.ToLower(h)
                            } else {
                                h = cont
                                lower = strings.ToLower(h)
                            }
                        }
                    }
                }

                if !strings.Contains(lower, "/maps/place/") {
                    continue
                }

                // normalize relative
                if strings.HasPrefix(h, "/") {
                    if u, err := url.Parse(resp.URL); err == nil && u.Scheme != "" && u.Host != "" {
                        h = u.Scheme + "://" + u.Host + h
                    }
                }

                nj := NewCroxyProxyJob(j.Job.ID, h)
                nj.Job.ParentID = j.Job.ID
                next = append(next, nj)
            }

            if len(next) > 0 {
                j.UsageInResults = false
                return nil, next, nil
            }
        }

        if raw, ok := resp.Meta["json"].([]byte); ok && len(raw) > 0 {
            entry, err := EntryFromJSON(raw)
            if err != nil {
                return nil, nil, err
            }

			if entry.Link == "" {
				entry.Link = resp.URL
			}

			// We have a concrete place entry -> include in results
			j.UsageInResults = true
			return &entry, nil, nil
		}
	}

	// Otherwise, treat as a search listing: spawn CroxyProxy sub-jobs for each place link
	if resp.Body != nil {
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(resp.Body)))
		if err == nil {
			var next []scrapemate.IJob

			// Prefer robust selector: any anchor linking to a Google Maps place page
			doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
				href := strings.TrimSpace(s.AttrOr("href", ""))
				if href == "" {
					// Croxy may rewrite to data-href
					href = strings.TrimSpace(s.AttrOr("data-href", ""))
				}
				if href == "" {
					return
				}

				// Only consider links to place pages
				lower := strings.ToLower(href)

				// Case 1: Croxy consent/share wrapper: /m?continue=<google-url>
				if strings.Contains(lower, "/m?continue=") {
					if u, err := url.Parse(href); err == nil {
						cont := u.Query().Get("continue")
						if cont != "" {
							// URL decode nested continue value
							if contDecoded, err2 := url.QueryUnescape(cont); err2 == nil {
								href = contDecoded
								lower = strings.ToLower(href)
							} else {
								href = cont
								lower = strings.ToLower(href)
							}
						}
					}
				}

				// Case 2: skip non-place links early
				if !strings.Contains(lower, "/maps/place/") {
					return
				}

				// Normalize relative URLs against the current response URL host
				if strings.HasPrefix(href, "/") {
					if u, err := url.Parse(resp.URL); err == nil && u.Scheme != "" && u.Host != "" {
						href = u.Scheme + "://" + u.Host + href
					}
				}

				nj := NewCroxyProxyJob(j.Job.ID, href)
				nj.Job.ParentID = j.Job.ID
				next = append(next, nj)
			})

			if len(next) > 0 {
				// This is a search page -> do not include this job in results
				j.UsageInResults = false
				return nil, next, nil
			}
		}
	}

	// Fallback: if current URL itself is a Croxy consent wrapper, try to spawn next job from its continue param
	if resp.URL != "" && strings.Contains(strings.ToLower(resp.URL), "/m?continue=") {
		if u, err := url.Parse(resp.URL); err == nil {
			if cont := u.Query().Get("continue"); cont != "" {
				if contDecoded, err2 := url.QueryUnescape(cont); err2 == nil {
					if strings.Contains(strings.ToLower(contDecoded), "/maps/place/") {
						nj := NewCroxyProxyJob(j.Job.ID, contDecoded)
						nj.Job.ParentID = j.Job.ID
						// This is a search/consent wrapper → no direct results
						j.UsageInResults = false
						return nil, []scrapemate.IJob{nj}, nil
					}
				}
			}
		}
	}

	return nil, nil, fmt.Errorf("croxy: no content retrieved or unsupported page type")
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

		if err := j.performCroxyProxyRequest(ctx, page); err != nil {
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

		// Use the actual final URL from the page, not the original target URL
		finalURL := page.URL()
		resp.URL = finalURL
		resp.StatusCode = 200
		resp.Body = []byte(content)

        // Attempt JSON extraction regardless of URL (Croxy often nests Maps in frames)
        if raw, err := ExtractJSONFromPage(page); err == nil && len(raw) > 0 {
            if resp.Meta == nil {
                resp.Meta = make(map[string]any)
            }
            resp.Meta["json"] = raw
        }

        // Collect place links from frames regardless of URL to robustly handle Croxy wrappers
        frames := page.Frames()
        var links []string

        for _, f := range frames {
            res, err := f.Evaluate(`(() => {
              const as = Array.from(document.querySelectorAll('a[href], a[data-href]'));
              return as.map(a => a.getAttribute('href') || a.getAttribute('data-href')).filter(Boolean);
            })()`)
            if err != nil {
                continue
            }
            if arr, ok := res.([]interface{}); ok {
                for _, it := range arr {
                    if s, ok := it.(string); ok {
                        ls := strings.ToLower(s)
                        if strings.Contains(ls, "/maps/place/") || strings.Contains(ls, "/m?continue=") {
                            links = append(links, s)
                        }
                    }
                }
            }
        }

        if len(links) > 0 {
            if resp.Meta == nil {
                resp.Meta = make(map[string]any)
            }
            resp.Meta["place_links"] = links
        }

		log.Info("CroxyProxy request successful")
		return resp
	}

	resp.Error = fmt.Errorf("failed to retrieve content after all retries")
	return resp
}

func (j *CroxyProxyJob) performCroxyProxyRequest(ctx context.Context, page playwright.Page) error {
    log := scrapemate.GetLoggerFromContext(ctx)
	// Note: User agent should be set at browser context level, not page level in Playwright Go

	// Set extra headers
	// Derive Accept-Language from target URL (hl parameter) if available
	if err := page.SetExtraHTTPHeaders(map[string]string{
		"Accept-Language": deriveAcceptLanguage(j.TargetURL),
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
	if _, err = page.WaitForSelector(urlInputSelector, playwright.PageWaitForSelectorOptions{
		Timeout: playwright.Float(defaultTimeout),
	}); err != nil {
		return fmt.Errorf("URL input field not found: %w", err)
	}

    // Clear and type target URL (normalize any IP-host Maps links to google.com)
    normalized := normalizeGoogleURL(j.TargetURL)
    if err := page.Fill(urlInputSelector, normalized); err != nil {
        return fmt.Errorf("failed to fill URL input: %w", err)
    }

	// Submit form and wait for navigation
	log.Info("Submitting form and waiting for navigation...")

	// Click submit button and wait for navigation
	if err := page.Click(submitButtonSelector); err != nil {
		return fmt.Errorf("failed to click submit button: %w", err)
	}

	// Wait for navigation
	if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State:   playwright.LoadStateDomcontentloaded,
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
		if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State:   playwright.LoadStateLoad,
			Timeout: playwright.Float(loadTimeout),
		}); err != nil {
			return fmt.Errorf("final redirect timeout: %w", err)
		}
		log.Info(fmt.Sprintf("Redirected successfully to: %s", page.URL()))
	} else {
		log.Info(fmt.Sprintf("Mapped directly to: %s", page.URL()))
	}

    // Handle Google consent using the shared handler (first attempt)
    if err := clickRejectCookiesIfRequired(page); err != nil {
        log.Info(fmt.Sprintf("Consent handling failed (continuing anyway): %v", err))
    }

	// If we are on Croxy consent wrapper (m?continue=...), wait until it redirects to the intended Google Maps URL
	if cur := page.URL(); strings.Contains(cur, "/m") {
		if u, err := url.Parse(cur); err == nil {
			if cont := u.Query().Get("continue"); cont != "" {
				log.Info("Waiting for redirect to Google Maps target from consent page...")
				_ = page.WaitForURL(cont, playwright.PageWaitForURLOptions{
					Timeout:   playwright.Float(navigationTimeout),
					WaitUntil: playwright.WaitUntilStateLoad,
				})
			}
		}
	}

	// Wait for CroxyProxy frame to render
	log.Info("Waiting for CroxyProxy frame to render...")
	_, err = page.WaitForSelector("#__cpsHeaderTab", playwright.PageWaitForSelectorOptions{
		Timeout: playwright.Float(defaultTimeout),
	})
	if err != nil {
		return fmt.Errorf("CroxyProxy frame not found: %w", err)
	}

	// Always wait for the actual target content to load, regardless of current URL
	log.Info("Waiting for target Google Maps content to load...")

	// Wait for Google Maps specific elements or URL change
	maxWaitTime := 60 * time.Second // Increased timeout
	startTime := time.Now()
	targetReached := false

	for time.Since(startTime) < maxWaitTime {
		currentURL := page.URL()

		// Check if we're now on Google Maps via CroxyProxy (IP-based URL)
		// CroxyProxy uses dynamic IP addresses like: https://51.159.195.122/maps/search/...
		if strings.Contains(currentURL, "/maps/search/") ||
			strings.Contains(currentURL, "/maps/place/") ||
			strings.Contains(currentURL, "google.com/maps") {
			log.Info(fmt.Sprintf("Successfully reached Google Maps at: %s", currentURL))
			targetReached = true
			break
		}

		// Check for Google Maps elements in the page content
		// Look for multiple indicators to be more reliable
		gmapsSelectors := []string{
			"div[role='main']",
			"div[data-value='Search']",
			"#searchboxinput",
			"div[data-value='Directions']",
			"div[aria-label*='Map']",
			"div[jsaction*='maps']",
		}

		foundElements := 0
		for _, selector := range gmapsSelectors {
			elements, _ := page.QuerySelectorAll(selector)
			if len(elements) > 0 {
				foundElements++
			}
		}

		// If we found multiple Google Maps elements, consider it successful
		if foundElements >= 2 {
			log.Info(fmt.Sprintf("Google Maps elements detected (%d indicators), target reached at: %s", foundElements, currentURL))
			targetReached = true
			break
		}

		// Also check page title for Google Maps
		title, _ := page.Title()
		if strings.Contains(strings.ToLower(title), "google maps") {
			log.Info(fmt.Sprintf("Google Maps title detected: %s", title))
			targetReached = true
			break
		}

		// Wait a bit before checking again
		time.Sleep(3 * time.Second)
	}

    // Final check - if we didn't reach the target, it's a failure
    if !targetReached {
        return fmt.Errorf("CroxyProxy failed to navigate to Google Maps content within %v seconds", maxWaitTime.Seconds())
    }

    // Second attempt to handle consent once target content is confirmed (consent can appear late)
    if err := clickRejectCookiesIfRequired(page); err != nil {
        log.Info(fmt.Sprintf("Post-target consent handling failed (continuing): %v", err))
    }

    log.Info("CroxyProxy frame rendered successfully")
    return nil
}

// normalizeGoogleURL ensures we pass a google.com Maps URL to Croxy, not an IP host
func normalizeGoogleURL(raw string) string {
    u, err := url.Parse(raw)
    if err != nil {
        return raw
    }

    // If this is a Croxy consent/share wrapper, unwrap the continue param first
    if strings.Contains(u.Path, "/m") {
        if cont := u.Query().Get("continue"); cont != "" {
            if cu, err2 := url.Parse(cont); err2 == nil {
                u = cu
            }
        }
    }

    host := u.Hostname()
    // If host is an IP or not a google domain, replace with www.google.com
    if net.ParseIP(host) != nil || !strings.Contains(strings.ToLower(host), "google.com") {
        u.Scheme = "https"
        u.Host = "www.google.com"
    }

    return u.String()
}

// handleGoogleConsent handles Google's cookie consent page by clicking "Accept all" or similar buttons

// deriveAcceptLanguage returns an Accept-Language header value based on the hl= parameter in the URL
func deriveAcceptLanguage(rawURL string) string {
	// Default language
	lang := "en"

	if u, err := url.Parse(rawURL); err == nil {
		// If it's a Croxy "m?continue=" URL, inspect the nested continue URL
		if strings.Contains(u.Path, "/m") {
			if cont := u.Query().Get("continue"); cont != "" {
				if cu, err2 := url.Parse(cont); err2 == nil {
					if hl := cu.Query().Get("hl"); hl != "" {
						lang = hl
					}
				}
			}
		}

		// Also check hl on the top-level URL
		if hl := u.Query().Get("hl"); hl != "" {
			lang = hl
		}
	}

	switch strings.ToLower(lang) {
	case "nl":
		return "nl-NL,nl;q=0.9,en-US;q=0.8"
	case "id":
		return "id-ID,id;q=0.9,en-US;q=0.8"
	case "de":
		return "de-DE,de;q=0.9,en-US;q=0.8"
	case "fr":
		return "fr-FR,fr;q=0.9,en-US;q=0.8"
	case "es":
		return "es-ES,es;q=0.9,en-US;q=0.8"
	default:
		return "en-US,en;q=0.9"
	}
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
