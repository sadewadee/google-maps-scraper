package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Nominatim public instance usage notes:
// - Identify your application with a proper User-Agent header (include URL or contact).
// - Respect rate limit: ~1 request per second per IP. We throttle to 1 rps here.
// - Cache results whenever possible; avoid bulk/batch queries to the public server.
// You can override defaults with:
//   GEOCODER_USER_AGENT: custom User-Agent string
//   NOMINATIM_BASE_URL: alternative base URL (self-hosted instance recommended)
//   GOOGLE_GEOCODING_API_KEY: API key for Google Geocoding (when provider = "google")

var (
	nominatimTicker = time.NewTicker(1 * time.Second) // 1 req/sec
	httpClient      = &http.Client{
		Timeout: 10 * time.Second,
	}
)

func ResolveAndFillBBox(ctx context.Context, d *JobData) error {
	// Nothing to resolve
	if strings.TrimSpace(d.Place) == "" {
		return nil
	}
	// If bbox already present, do not override
	if d.BboxMinLat != "" || d.BboxMinLon != "" || d.BboxMaxLat != "" || d.BboxMaxLon != "" {
		return nil
	}

	provider := strings.ToLower(strings.TrimSpace(d.Provider))
	if provider == "" {
		provider = "nominatim"
	}

	var (
		minLat, minLon, maxLat, maxLon string
		err                            error
	)

	switch provider {
	case "google":
		apiKey := strings.TrimSpace(d.GoogleAPIKey)
		if apiKey == "" {
			apiKey = strings.TrimSpace(os.Getenv("GOOGLE_GEOCODING_API_KEY"))
		}
		if apiKey == "" {
			return fmt.Errorf("google geocoding selected but missing API key")
		}
		minLat, minLon, maxLat, maxLon, err = geocodeGoogle(ctx, d.Place, d.CC, apiKey)
	default: // "nominatim"
		minLat, minLon, maxLat, maxLon, err = geocodeNominatim(ctx, d.Place, d.CC)
	}

	if err != nil {
		return fmt.Errorf("geocoding failed: %w", err)
	}

	d.BboxMinLat = minLat
	d.BboxMinLon = minLon
	d.BboxMaxLat = maxLat
	d.BboxMaxLon = maxLon
	return nil
}

func geocodeNominatim(ctx context.Context, place, cc string) (minLat, minLon, maxLat, maxLon string, err error) {
	// Rate limit to 1 req/sec
	select {
	case <-ctx.Done():
		return "", "", "", "", ctx.Err()
	case <-nominatimTicker.C:
	}

	base := strings.TrimSpace(os.Getenv("NOMINATIM_BASE_URL"))
	if base == "" {
		base = "https://nominatim.openstreetmap.org/search"
	}
	q := url.Values{}
	q.Set("format", "jsonv2")
	q.Set("limit", "1")
	q.Set("addressdetails", "0")
	q.Set("q", place)
	if cc = strings.TrimSpace(cc); cc != "" {
		q.Set("countrycodes", strings.ToLower(cc))
	}
	u := base + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", "", "", err
	}
	ua := strings.TrimSpace(os.Getenv("GEOCODER_USER_AGENT"))
	if ua == "" {
		ua = "google-maps-scraper/1.0 (+https://github.com/gosom/google-maps-scraper)"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", "", "", fmt.Errorf("nominatim status: %s", resp.Status)
	}

	var arr []struct {
		Boundingbox []string `json:"boundingbox"` // [south, north, west, east] as strings
	}
	if err := json.NewDecoder(resp.Body).Decode(&arr); err != nil {
		return "", "", "", "", err
	}
	if len(arr) == 0 || len(arr[0].Boundingbox) != 4 {
		return "", "", "", "", fmt.Errorf("nominatim: no bounding box found")
	}
	south := strings.TrimSpace(arr[0].Boundingbox[0])
	north := strings.TrimSpace(arr[0].Boundingbox[1])
	west := strings.TrimSpace(arr[0].Boundingbox[2])
	east := strings.TrimSpace(arr[0].Boundingbox[3])

	return south, west, north, east, nil
}

func geocodeGoogle(ctx context.Context, place, cc, apiKey string) (minLat, minLon, maxLat, maxLon string, err error) {
	q := url.Values{}
	q.Set("address", place)
	q.Set("key", apiKey)
	if cc = strings.TrimSpace(cc); cc != "" {
		// components supports country filter
		q.Set("components", "country:"+cc)
	}
	u := "https://maps.googleapis.com/maps/api/geocode/json?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", "", "", err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", "", "", fmt.Errorf("google geocoding status: %s", resp.Status)
	}

	var out struct {
		Status  string `json:"status"`
		Results []struct {
			Geometry struct {
				Bounds struct {
					NE struct {
						Lat float64 `json:"lat"`
						Lng float64 `json:"lng"`
					} `json:"northeast"`
					SW struct {
						Lat float64 `json:"lat"`
						Lng float64 `json:"lng"`
					} `json:"southwest"`
				} `json:"bounds"`
				Viewport struct {
					NE struct {
						Lat float64 `json:"lat"`
						Lng float64 `json:"lng"`
					} `json:"northeast"`
					SW struct {
						Lat float64 `json:"lat"`
						Lng float64 `json:"lng"`
					} `json:"southwest"`
				} `json:"viewport"`
			} `json:"geometry"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", "", "", err
	}
	if strings.ToUpper(out.Status) != "OK" || len(out.Results) == 0 {
		return "", "", "", "", fmt.Errorf("google geocoding error: %s", out.Status)
	}

	g := out.Results[0].Geometry
	var swLat, swLng, neLat, neLng float64

	// Prefer bounds; fallback to viewport when bounds is missing
	if g.Bounds.NE.Lat != 0 || g.Bounds.NE.Lng != 0 || g.Bounds.SW.Lat != 0 || g.Bounds.SW.Lng != 0 {
		swLat, swLng = g.Bounds.SW.Lat, g.Bounds.SW.Lng
		neLat, neLng = g.Bounds.NE.Lat, g.Bounds.NE.Lng
	} else {
		swLat, swLng = g.Viewport.SW.Lat, g.Viewport.SW.Lng
		neLat, neLng = g.Viewport.NE.Lat, g.Viewport.NE.Lng
	}

	return fmt.Sprintf("%f", swLat), fmt.Sprintf("%f", swLng), fmt.Sprintf("%f", neLat), fmt.Sprintf("%f", neLng), nil
}
