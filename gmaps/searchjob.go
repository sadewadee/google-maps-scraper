package gmaps

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/deduper"
	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/scrapemate"
)

type SearchJobOptions func(*SearchJob)

type MapLocation struct {
	Lat     float64
	Lon     float64
	ZoomLvl float64
	Radius  float64
}

type MapSearchParams struct {
	Location  MapLocation
	Query     string
	ViewportW int
	ViewportH int
	Hl        string

	// Adaptive tiling controls
	SplitThreshold int // threshold to trigger subdivision
	MinZoom        int // starting zoom
	MaxZoom        int // maximum zoom for subdivision
	SubdivLevel    int // current subdivision level

	// Strategy
	StaticFirst bool // prefer static pb path first (currently informational for seeds)
}

type SearchJob struct {
	scrapemate.Job

	params      *MapSearchParams
	ExitMonitor exiter.Exiter
	Deduper     deduper.Deduper
}

func NewSearchJob(params *MapSearchParams, opts ...SearchJobOptions) *SearchJob {
	const (
		defaultPrio       = scrapemate.PriorityMedium
		defaultMaxRetries = 3
		baseURL           = "https://maps.google.com/search"
	)

	job := SearchJob{
		Job: scrapemate.Job{
			ID:         uuid.New().String(),
			Method:     http.MethodGet,
			URL:        baseURL,
			URLParams:  buildGoogleMapsParams(params),
			MaxRetries: defaultMaxRetries,
			Priority:   defaultPrio,
		},
	}

	job.params = params

	for _, opt := range opts {
		opt(&job)
	}

	return &job
}

func WithSearchJobExitMonitor(exitMonitor exiter.Exiter) SearchJobOptions {
	return func(j *SearchJob) {
		j.ExitMonitor = exitMonitor
	}
}

func WithSearchJobDeduper(d deduper.Deduper) SearchJobOptions {
	return func(j *SearchJob) {
		j.Deduper = d
	}
}

func (j *SearchJob) Process(ctx context.Context, resp *scrapemate.Response) (any, []scrapemate.IJob, error) {
	defer func() {
		resp.Document = nil
		resp.Body = nil
		resp.Meta = nil
	}()

	t0 := time.Now()
	log := scrapemate.GetLoggerFromContext(ctx)

	body := removeFirstLine(resp.Body)
	if len(body) == 0 {
		// Fallback: no body (unexpected) → schedule browser job
		var next []scrapemate.IJob
		currentZoom := int(j.params.Location.ZoomLvl)

		coords := fmt.Sprintf("%.6f,%.6f", j.params.Location.Lat, j.params.Location.Lon)
		gopts := []GmapJobOptions{}
		if j.ExitMonitor != nil {
			gopts = append(gopts, WithExitMonitor(j.ExitMonitor))
		}
		if j.Deduper != nil {
			gopts = append(gopts, WithDeduper(j.Deduper))
		}

		fallbackDepth := 10
		fallbackEmail := false

		next = append(next, NewGmapJob(
			"",
			j.params.Hl,
			j.params.Query,
			fallbackDepth,
			fallbackEmail,
			coords,
			currentZoom,
			gopts...,
		))

		if j.ExitMonitor != nil {
			j.ExitMonitor.IncrSeedCompleted(1)
		}

		if log != nil {
			elapsed := time.Since(t0)
			log.Info(fmt.Sprintf("tile_fallback reason=empty_body q=%q lat=%.6f lon=%.6f zoom=%d dur=%s",
				j.params.Query, j.params.Location.Lat, j.params.Location.Lon, currentZoom, elapsed.String()))
		}

		return nil, next, nil
	}

	entries, err := ParseSearchResults(body)
	if err != nil {
		// Fallback: parse failure → schedule browser job
		var next []scrapemate.IJob
		currentZoom := int(j.params.Location.ZoomLvl)

		coords := fmt.Sprintf("%.6f,%.6f", j.params.Location.Lat, j.params.Location.Lon)
		gopts := []GmapJobOptions{}
		if j.ExitMonitor != nil {
			gopts = append(gopts, WithExitMonitor(j.ExitMonitor))
		}
		if j.Deduper != nil {
			gopts = append(gopts, WithDeduper(j.Deduper))
		}

		fallbackDepth := 10
		fallbackEmail := false

		next = append(next, NewGmapJob(
			"",
			j.params.Hl,
			j.params.Query,
			fallbackDepth,
			fallbackEmail,
			coords,
			currentZoom,
			gopts...,
		))

		if j.ExitMonitor != nil {
			j.ExitMonitor.IncrSeedCompleted(1)
		}

		if log != nil {
			elapsed := time.Since(t0)
			log.Info(fmt.Sprintf("tile_fallback reason=parse_failure q=%q lat=%.6f lon=%.6f zoom=%d err=%q dur=%s",
				j.params.Query, j.params.Location.Lat, j.params.Location.Lon, currentZoom, err.Error(), elapsed.String()))
		}

		return nil, next, nil
	}

	// Fallback to browser path when static returns no or single entry
	if len(entries) == 0 {
		var next []scrapemate.IJob
		currentZoom := int(j.params.Location.ZoomLvl)

		coords := fmt.Sprintf("%.6f,%.6f", j.params.Location.Lat, j.params.Location.Lon)
		gopts := []GmapJobOptions{}
		if j.ExitMonitor != nil {
			gopts = append(gopts, WithExitMonitor(j.ExitMonitor))
		}
		if j.Deduper != nil {
			gopts = append(gopts, WithDeduper(j.Deduper))
		}

		fallbackDepth := 10
		fallbackEmail := false

		next = append(next, NewGmapJob(
			"",
			j.params.Hl,
			j.params.Query,
			fallbackDepth,
			fallbackEmail,
			coords,
			currentZoom,
			gopts...,
		))

		if j.ExitMonitor != nil {
			j.ExitMonitor.IncrSeedCompleted(1)
		}

		if log != nil {
			elapsed := time.Since(t0)
			log.Info(fmt.Sprintf("tile_fallback reason=no_entries q=%q lat=%.6f lon=%.6f zoom=%d dur=%s",
				j.params.Query, j.params.Location.Lat, j.params.Location.Lon, currentZoom, elapsed.String()))
		}

		return nil, next, nil
	}
	if len(entries) == 1 {
		var next []scrapemate.IJob
		currentZoom := int(j.params.Location.ZoomLvl)

		coords := fmt.Sprintf("%.6f,%.6f", j.params.Location.Lat, j.params.Location.Lon)
		gopts := []GmapJobOptions{}
		if j.ExitMonitor != nil {
			gopts = append(gopts, WithExitMonitor(j.ExitMonitor))
		}
		if j.Deduper != nil {
			gopts = append(gopts, WithDeduper(j.Deduper))
		}

		fallbackDepth := 10
		fallbackEmail := false

		next = append(next, NewGmapJob(
			"",
			j.params.Hl,
			j.params.Query,
			fallbackDepth,
			fallbackEmail,
			coords,
			currentZoom,
			gopts...,
		))

		if j.ExitMonitor != nil {
			j.ExitMonitor.IncrSeedCompleted(1)
		}

		if log != nil {
			elapsed := time.Since(t0)
			log.Info(fmt.Sprintf("tile_fallback reason=single_place q=%q lat=%.6f lon=%.6f zoom=%d dur=%s",
				j.params.Query, j.params.Location.Lat, j.params.Location.Lon, currentZoom, elapsed.String()))
		}

		return nil, next, nil
	}
	// Radius filter if provided
	if j.params.Location.Radius > 0 {
		entries = filterAndSortEntriesWithinRadius(entries,
			j.params.Location.Lat,
			j.params.Location.Lon,
			j.params.Location.Radius,
		)
	}

	// Adaptive subdivision: if saturated and zoom can increase, spawn child jobs
	var next []scrapemate.IJob
	currentZoom := int(j.params.Location.ZoomLvl)
	if j.params.SplitThreshold > 0 && len(entries) >= j.params.SplitThreshold && currentZoom < j.params.MaxZoom {
		// Compute viewport meters at current zoom and offsets for 2x2 coverage
		w := j.params.ViewportW
		h := j.params.ViewportH
		if w <= 0 {
			w = 600
		}
		if h <= 0 {
			h = 800
		}
		vwMeters, vhMeters := viewportMeters(j.params.Location.Lat, currentZoom, w, h)
		halfW := vwMeters / 2
		halfH := vhMeters / 2

		lat := j.params.Location.Lat
		lon := j.params.Location.Lon

		// Convert meter offsets to degrees
		latOffsetDeg := halfH / 110540.0 // meters per degree latitude ~110.54 km
		cosLat := math.Cos(lat * math.Pi / 180.0)
		lonMetersPerDeg := 111320.0 * cosLat
		if lonMetersPerDeg == 0 {
			lonMetersPerDeg = 1 // safeguard
		}
		lonOffsetDeg := halfW / lonMetersPerDeg

		// 2x2 child centers
		childCenters := []MapLocation{
			{Lat: lat + latOffsetDeg, Lon: lon - lonOffsetDeg, ZoomLvl: float64(currentZoom + 1), Radius: j.params.Location.Radius},
			{Lat: lat + latOffsetDeg, Lon: lon + lonOffsetDeg, ZoomLvl: float64(currentZoom + 1), Radius: j.params.Location.Radius},
			{Lat: lat - latOffsetDeg, Lon: lon - lonOffsetDeg, ZoomLvl: float64(currentZoom + 1), Radius: j.params.Location.Radius},
			{Lat: lat - latOffsetDeg, Lon: lon + lonOffsetDeg, ZoomLvl: float64(currentZoom + 1), Radius: j.params.Location.Radius},
		}

		for _, c := range childCenters {
			p := &MapSearchParams{
				Location:       c,
				Query:          j.params.Query,
				ViewportW:      w,
				ViewportH:      h,
				Hl:             j.params.Hl,
				SplitThreshold: j.params.SplitThreshold,
				MinZoom:        j.params.MinZoom,
				MaxZoom:        j.params.MaxZoom,
				SubdivLevel:    j.params.SubdivLevel + 1,
			}
			opts := []SearchJobOptions{}
			if j.ExitMonitor != nil {
				opts = append(opts, WithSearchJobExitMonitor(j.ExitMonitor))
			}
			next = append(next, NewSearchJob(p, opts...))
		}

		// Count only seed completion for saturated tile
		if j.ExitMonitor != nil {
			j.ExitMonitor.IncrSeedCompleted(1)
		}

		if log != nil {
			elapsed := time.Since(t0)
			log.Info(fmt.Sprintf("tile_subdivide q=%q lat=%.6f lon=%.6f zoom=%d entries=%d children=%d dur=%s",
				j.params.Query, j.params.Location.Lat, j.params.Location.Lon, currentZoom, len(entries), len(next), elapsed.String()))
		}

		return nil, next, nil
	}

	// Below threshold: accept entries (apply dedup when available)
	if j.Deduper != nil {
		unique := make([]*Entry, 0, len(entries))
		for _, e := range entries {
			key := BuildEntryKey(e)
			if key == "" || j.Deduper.AddIfNotExists(ctx, key) {
				unique = append(unique, e)
			}
		}
		entries = unique
	}

	if j.ExitMonitor != nil {
		j.ExitMonitor.IncrSeedCompleted(1)
		j.ExitMonitor.IncrPlacesFound(len(entries))
		j.ExitMonitor.IncrPlacesCompleted(len(entries))
	}

	if log != nil {
		elapsed := time.Since(t0)
		log.Info(fmt.Sprintf("tile_accept q=%q lat=%.6f lon=%.6f zoom=%d entries=%d dur=%s",
			j.params.Query, j.params.Location.Lat, j.params.Location.Lon, int(j.params.Location.ZoomLvl), len(entries), elapsed.String()))
	}

	return entries, nil, nil
}

func removeFirstLine(data []byte) []byte {
	if len(data) == 0 {
		return data
	}

	index := bytes.IndexByte(data, '\n')
	if index == -1 {
		return []byte{}
	}

	return data[index+1:]
}

func buildGoogleMapsParams(params *MapSearchParams) map[string]string {
	// Respect provided viewport; default if zero
	if params.ViewportH == 0 {
		params.ViewportH = 800
	}
	if params.ViewportW == 0 {
		params.ViewportW = 600
	}

	ans := map[string]string{
		"tbm":      "map",
		"authuser": "0",
		"hl":       params.Hl,
		"q":        params.Query,
	}

	pb := fmt.Sprintf("!4m12!1m3!1d3826.902183192154!2d%.4f!3d%.4f!2m3!1f0!2f0!3f0!3m2!1i%d!2i%d!4f%.1f!7i20!8i0"+
		"!10b1!12m22!1m3!18b1!30b1!34e1!2m3!5m1!6e2!20e3!4b0!10b1!12b1!13b1!16b1!17m1!3e1!20m3!5e2!6b1!14b1!46m1!1b0"+
		"!96b1!19m4!2m3!1i360!2i120!4i8",
		params.Location.Lon,
		params.Location.Lat,
		params.ViewportW,
		params.ViewportH,
		params.Location.ZoomLvl,
	)

	ans["pb"] = pb

	return ans
}

// metersPerPixel returns meters per pixel at a given latitude and zoom.
func metersPerPixel(lat float64, zoom int) float64 {
	// Web Mercator approximation
	return math.Cos(lat*math.Pi/180.0) * 2 * math.Pi * 6378137.0 / (256.0 * math.Pow(2.0, float64(zoom)))
}

// viewportMeters returns approximate viewport width and height in meters.
func viewportMeters(lat float64, zoom, vw, vh int) (float64, float64) {
	mpp := metersPerPixel(lat, zoom)
	return mpp * float64(vw), mpp * float64(vh)
}
