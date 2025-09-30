package gmaps

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gosom/scrapemate"
	"github.com/playwright-community/playwright-go"

	"github.com/sadewadee/google-maps-scraper/exiter"
)

// EmailPreflightJobOptions configures EmailPreflightJob.
type EmailPreflightJobOptions func(*EmailPreflightJob)

type EmailPreflightJob struct {
	scrapemate.Job

	Entry       *Entry
	ExitMonitor exiter.Exiter

	// Fast timeouts (ms). Focus on BIG-data performance.
	DNSTimeoutMs  int
	TCPTimeoutMs  int
	HEADTimeoutMs int
	EnableHEAD    bool

	useInResults bool
}

// Defaults tuned for performance.
const (
	defaultDNSTimeoutMs  = 300
	defaultTCPTimeoutMs  = 500
	defaultHEADTimeoutMs = 800
)

// social domains to skip (not useful for email extraction)
var socialDomains = []string{
	"facebook.com",
	"instagram.com",
	"twitter.com",
	"tiktok.com",
	"wa.me",
	"linktr.ee",
}

// In-memory TTL cache for domain liveness
type preflightCacheEntry struct {
	alive   bool
	expires time.Time
}

var (
	preflightCacheTTL = 15 * time.Minute
	preflightCacheMu  sync.RWMutex
	preflightCache    = make(map[string]preflightCacheEntry)
)

func cacheGet(domain string) (alive bool, ok bool) {
	preflightCacheMu.RLock()
	defer preflightCacheMu.RUnlock()
	ent, exists := preflightCache[domain]
	if !exists {
		return false, false
	}
	if time.Now().After(ent.expires) {
		return false, false
	}
	return ent.alive, true
}

func cacheSet(domain string, alive bool) {
	preflightCacheMu.Lock()
	defer preflightCacheMu.Unlock()
	preflightCache[domain] = preflightCacheEntry{
		alive:   alive,
		expires: time.Now().Add(preflightCacheTTL),
	}
}

func NewEmailPreflightJob(parentID string, entry *Entry, opts ...EmailPreflightJobOptions) *EmailPreflightJob {
	const (
		defaultPrio       = scrapemate.PriorityMedium
		defaultMaxRetries = 0
	)

	job := EmailPreflightJob{
		Job: scrapemate.Job{
			ID:         uuid.New().String(),
			ParentID:   parentID,
			Method:     "LOCAL",       // compute-only, minimal network probes
			URL:        "about:blank", // valid no-op URL to avoid Playwright invalid URL navigation
			MaxRetries: defaultMaxRetries,
			Priority:   defaultPrio,
		},
		Entry:         entry,
		DNSTimeoutMs:  defaultDNSTimeoutMs,
		TCPTimeoutMs:  defaultTCPTimeoutMs,
		HEADTimeoutMs: defaultHEADTimeoutMs,
		EnableHEAD:    false, // disabled by default for perf
		useInResults:  true,  // finalize when short-circuited
	}

	for _, opt := range opts {
		opt(&job)
	}

	return &job
}

func WithEmailPreflightExitMonitor(exitMonitor exiter.Exiter) EmailPreflightJobOptions {
	return func(j *EmailPreflightJob) {
		j.ExitMonitor = exitMonitor
	}
}

func WithEmailPreflightTimeouts(dnsMs, tcpMs, headMs int, enableHead bool) EmailPreflightJobOptions {
	return func(j *EmailPreflightJob) {
		if dnsMs > 0 {
			j.DNSTimeoutMs = dnsMs
		}
		if tcpMs > 0 {
			j.TCPTimeoutMs = tcpMs
		}
		if headMs > 0 {
			j.HEADTimeoutMs = headMs
		}
		j.EnableHEAD = enableHead
	}
}

func (j *EmailPreflightJob) Process(ctx context.Context, _ *scrapemate.Response) (any, []scrapemate.IJob, error) {
	log := scrapemate.GetLoggerFromContext(ctx)

	defer func() {
		if j.ExitMonitor != nil {
			// preflight completes a place path regardless of outcome
			j.ExitMonitor.IncrPlacesCompleted(1)
		}
	}()

	website := strings.TrimSpace(j.Entry.WebSite)
	if website == "" {
		// nothing to do, finalize entry
		j.useInResults = true
		if log != nil {
			log.Info("preflight_skip reason=empty_url")
		}
		return j.Entry, nil, nil
	}
	if !hasHTTPScheme(website) {
		j.useInResults = true
		if log != nil {
			log.Info("preflight_skip reason=unsupported_scheme", "url", website)
		}
		return j.Entry, nil, nil
	}

	u, err := url.Parse(website)
	if err != nil || u.Host == "" {
		j.useInResults = true
		if log != nil {
			log.Info("preflight_skip reason=parse_error", "url", website, "err", fmt.Sprintf("%v", err))
		}
		return j.Entry, nil, nil
	}

	host := hostOnly(u.Host)
	if isSocialDomain(host) {
		// social domains are skipped for email extraction
		j.useInResults = true
		if log != nil {
			log.Info("preflight_skip reason=social_domain", "host", host)
		}
		return j.Entry, nil, nil
	}

	// Cache check
	if alive, ok := cacheGet(host); ok {
		if alive {
			return j.chainToEmail(ctx)
		}
		// dead cached
		j.useInResults = true
		if log != nil {
			log.Info("preflight_dead_cache", "host", host)
		}
		return j.Entry, nil, nil
	}

	// DNS resolve
	dnsCtx, cancelDNS := context.WithTimeout(ctx, time.Duration(j.DNSTimeoutMs)*time.Millisecond)
	defer cancelDNS()
	ips, dnsErr := net.DefaultResolver.LookupIP(dnsCtx, "ip", host)
	if dnsErr != nil || len(ips) == 0 {
		cacheSet(host, false)
		j.useInResults = true
		if log != nil {
			log.Info("preflight_dead_dns", "host", host, "err", fmt.Sprintf("%v", dnsErr))
		}
		return j.Entry, nil, nil
	}

	// TCP connect (443 then 80)
	if !quickTCPConnect(ctx, host, j.TCPTimeoutMs) {
		cacheSet(host, false)
		j.useInResults = true
		if log != nil {
			log.Info("preflight_dead_tcp", "host", host)
		}
		return j.Entry, nil, nil
	}

	// Optional HEAD request (disabled by default)
	if j.EnableHEAD {
		if !quickHEAD(ctx, u, j.HEADTimeoutMs) {
			cacheSet(host, false)
			j.useInResults = true
			if log != nil {
				log.Info("preflight_dead_head", "host", host)
			}
			return j.Entry, nil, nil
		}
	}

	// Alive
	cacheSet(host, true)
	if log != nil {
		log.Info("preflight_alive", "host", host)
		log.Info("Added the URL Preflight", "url", website)
	}
	return j.chainToEmail(ctx)
}

func (j *EmailPreflightJob) ProcessOnFetchError() bool {
	return true
}

// UseInResults: true when short-circuit finalization; false when chaining.
func (j *EmailPreflightJob) UseInResults() bool {
	return j.useInResults
}

// BrowserActions is a no-op for compute-only LOCAL jobs to avoid jshttp calling the embedded default.
// It returns a minimal successful Response without using the browser page.
func (j *EmailPreflightJob) BrowserActions(_ context.Context, _ playwright.Page) scrapemate.Response {
	var resp scrapemate.Response
	resp.URL = j.GetURL()
	resp.StatusCode = http.StatusOK
	resp.Headers = make(http.Header)
	return resp
}

func (j *EmailPreflightJob) chainToEmail(ctx context.Context) (any, []scrapemate.IJob, error) {
	j.useInResults = false
	opts := []EmailExtractJobOptions{}
	if j.ExitMonitor != nil {
		opts = append(opts, WithEmailJobExitMonitor(j.ExitMonitor))
	}
	emailJob := NewEmailJob(j.ID, j.Entry, opts...)
	if lg := scrapemate.GetLoggerFromContext(ctx); lg != nil {
		lg.Info("preflight_chain_to_email", "url", j.Entry.WebSite)
	}
	return nil, []scrapemate.IJob{emailJob}, nil
}

func hasHTTPScheme(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func isSocialDomain(host string) bool {
	lh := strings.ToLower(host)
	for _, d := range socialDomains {
		if strings.Contains(lh, d) {
			return true
		}
	}
	return false
}

func hostOnly(h string) string {
	// strip port if present
	if i := strings.LastIndex(h, ":"); i > -1 {
		return h[:i]
	}
	return h
}

func quickTCPConnect(ctx context.Context, host string, timeoutMs int) bool {
	// Try 443 then 80
	dialer := &net.Dialer{
		Timeout:   time.Duration(timeoutMs) * time.Millisecond,
		KeepAlive: -1, // disable
	}

	// Prefer TLS 443
	// Minimal TLS dial (client hello) to check reachability quickly.
	tlsConf := &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         host,
	}
	// TLS dial uses dialer timeout; no separate context needed
	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(host, "443"), tlsConf)
	if err == nil && conn != nil {
		_ = conn.Close()
		return true
	}

	// Fallback to plain TCP 80
	tcpCtx, cancelTCP := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancelTCP()
	conn2, err2 := dialer.DialContext(tcpCtx, "tcp", net.JoinHostPort(host, "80"))
	if err2 == nil && conn2 != nil {
		_ = conn2.Close()
		return true
	}

	return false
}

func quickHEAD(ctx context.Context, u *url.URL, timeoutMs int) bool {
	client := &http.Client{
		Timeout: time.Duration(timeoutMs) * time.Millisecond,
		Transport: &http.Transport{
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
			DisableKeepAlives:     true,
			MaxIdleConns:          0,
			IdleConnTimeout:       0,
			ExpectContinueTimeout: 0,
			ResponseHeaderTimeout: time.Duration(timeoutMs) * time.Millisecond,
		},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// consider 200-399 alive
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}
