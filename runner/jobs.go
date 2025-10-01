package runner

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"plugin"
	"strconv"
	"strings"

	"github.com/gosom/scrapemate"
	"github.com/sadewadee/google-maps-scraper/deduper"
	"github.com/sadewadee/google-maps-scraper/exiter"
	"github.com/sadewadee/google-maps-scraper/gmaps"
)

func CreateSeedJobs(
	fastmode bool,
	langCode string,
	r io.Reader,
	maxDepth int,
	email bool,
	geoCoordinates string,
	zoom int,
	radius float64,
	dedup deduper.Deduper,
	exitMonitor exiter.Exiter,
	extraReviews bool,
	// Preflight controls (propagated into Gmap/Place pipeline)
	preflightEnabled bool,
	preflightDNSTimeoutMs int,
	preflightTCPTimeoutMs int,
	preflightHEADTimeoutMs int,
	preflightEnableHEAD bool,
) (jobs []scrapemate.IJob, err error) {
	var lat, lon float64

	if fastmode {
		if geoCoordinates == "" {
			return nil, fmt.Errorf("geo coordinates are required in fast mode")
		}

		parts := strings.Split(geoCoordinates, ",")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid geo coordinates: %s", geoCoordinates)
		}

		lat, err = strconv.ParseFloat(parts[0], 64)
		if err != nil {
			return nil, fmt.Errorf("invalid latitude: %w", err)
		}

		lon, err = strconv.ParseFloat(parts[1], 64)
		if err != nil {
			return nil, fmt.Errorf("invalid longitude: %w", err)
		}

		if lat < -90 || lat > 90 {
			return nil, fmt.Errorf("invalid latitude: %f", lat)
		}

		if lon < -180 || lon > 180 {
			return nil, fmt.Errorf("invalid longitude: %f", lon)
		}

		if zoom < 1 || zoom > 21 {
			return nil, fmt.Errorf("invalid zoom level: %d", zoom)
		}

		if radius < 0 {
			return nil, fmt.Errorf("invalid radius: %f", radius)
		}
	}

	scanner := bufio.NewScanner(r)

	for scanner.Scan() {
		query := strings.TrimSpace(scanner.Text())
		if query == "" {
			continue
		}

		var id string

		if before, after, ok := strings.Cut(query, "#!#"); ok {
			query = strings.TrimSpace(before)
			id = strings.TrimSpace(after)
		}

		var job scrapemate.IJob

		if !fastmode {
			opts := []gmaps.GmapJobOptions{}

			if dedup != nil {
				opts = append(opts, gmaps.WithDeduper(dedup))
			}

			if exitMonitor != nil {
				opts = append(opts, gmaps.WithExitMonitor(exitMonitor))
			}

			if extraReviews {
				opts = append(opts, gmaps.WithExtraReviews())
			}

			// Propagate preflight settings from web job data into Gmap/Place pipeline
			opts = append(opts, gmaps.WithPreflightEnabled(preflightEnabled))
			opts = append(opts, gmaps.WithPreflightConfig(preflightDNSTimeoutMs, preflightTCPTimeoutMs, preflightHEADTimeoutMs, preflightEnableHEAD))

			job = gmaps.NewGmapJob(id, langCode, query, maxDepth, email, geoCoordinates, zoom, opts...)
		} else {
			jparams := gmaps.MapSearchParams{
				Location: gmaps.MapLocation{
					Lat:     lat,
					Lon:     lon,
					ZoomLvl: float64(zoom),
					Radius:  radius,
				},
				Query:     query,
				ViewportW: 1920,
				ViewportH: 450,
				Hl:        langCode,
			}

			opts := []gmaps.SearchJobOptions{}

			if exitMonitor != nil {
				opts = append(opts, gmaps.WithSearchJobExitMonitor(exitMonitor))
			}
			if dedup != nil {
				opts = append(opts, gmaps.WithSearchJobDeduper(dedup))
			}
			// propagate preflight config into SearchJob so browser fallbacks carry it downstream
			opts = append(opts, gmaps.WithSearchJobPreflight(preflightEnabled, preflightDNSTimeoutMs, preflightTCPTimeoutMs, preflightHEADTimeoutMs, preflightEnableHEAD))

			job = gmaps.NewSearchJob(&jparams, opts...)
		}

		jobs = append(jobs, job)
	}

	return jobs, scanner.Err()
}

func LoadCustomWriter(pluginDir, pluginName string) (scrapemate.ResultWriter, error) {
	files, err := os.ReadDir(pluginDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read plugin directory: %w", err)
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		if filepath.Ext(file.Name()) != ".so" && filepath.Ext(file.Name()) != ".dll" {
			continue
		}

		pluginPath := filepath.Join(pluginDir, file.Name())

		p, err := plugin.Open(pluginPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open plugin %s: %w", file.Name(), err)
		}

		symWriter, err := p.Lookup(pluginName)
		if err != nil {
			return nil, fmt.Errorf("failed to lookup symbol %s: %w", pluginName, err)
		}

		writer, ok := symWriter.(*scrapemate.ResultWriter)
		if !ok {
			return nil, fmt.Errorf("unexpected type %T from writer symbol in plugin %s", symWriter, file.Name())
		}

		return *writer, nil
	}

	return nil, fmt.Errorf("no plugin found in %s", pluginDir)
}

// CreateTiledSeedJobs generates static pb SearchJob seeds by covering a bounding box with a simple adaptive grid.
// It emits initial tiles at minZoom; each SearchJob can further subdivide at runtime when saturated based on SplitThreshold.
func CreateTiledSeedJobs(
	langCode string,
	r io.Reader,
	bboxMinLat, bboxMinLon, bboxMaxLat, bboxMaxLon string,
	minZoom int,
	maxZoom int,
	splitThreshold int,
	maxTiles int,
	staticFirst bool,
	radius float64,
	dedup deduper.Deduper,
	exitMonitor exiter.Exiter,
	preflightEnabled bool,
	preflightDNSTimeoutMs int,
	preflightTCPTimeoutMs int,
	preflightHEADTimeoutMs int,
	preflightEnableHEAD bool,
) (jobs []scrapemate.IJob, err error) {
	// Parse bbox coordinates
	minLat, err := strconv.ParseFloat(strings.TrimSpace(bboxMinLat), 64)
	if err != nil {
		return nil, fmt.Errorf("invalid bbox_min_lat: %w", err)
	}
	minLon, err := strconv.ParseFloat(strings.TrimSpace(bboxMinLon), 64)
	if err != nil {
		return nil, fmt.Errorf("invalid bbox_min_lon: %w", err)
	}
	maxLat, err := strconv.ParseFloat(strings.TrimSpace(bboxMaxLat), 64)
	if err != nil {
		return nil, fmt.Errorf("invalid bbox_max_lat: %w", err)
	}
	maxLon, err := strconv.ParseFloat(strings.TrimSpace(bboxMaxLon), 64)
	if err != nil {
		return nil, fmt.Errorf("invalid bbox_max_lon: %w", err)
	}

	if minLat >= maxLat || minLon >= maxLon {
		return nil, fmt.Errorf("invalid bbox: min must be less than max")
	}

	if minZoom < 1 {
		minZoom = 10
	}
	if maxZoom < minZoom {
		maxZoom = minZoom
	}
	if maxZoom > 21 {
		maxZoom = 21
	}
	if splitThreshold <= 0 {
		splitThreshold = 90
	}
	if maxTiles <= 0 {
		maxTiles = 250000
	}

	// Decide grid dimensions based on maxTiles (square grid).
	n := intSqrt(maxTiles)
	if n < 1 {
		n = 1
	}
	// Cap grid size to avoid excessive initial seeds; SearchJob will subdivide when needed.
	if n > 256 {
		n = 256
	}

	latSpan := maxLat - minLat
	lonSpan := maxLon - minLon
	stepLat := latSpan / float64(n)
	stepLon := lonSpan / float64(n)

	// Prepare keyword scanner
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		query := strings.TrimSpace(scanner.Text())
		if query == "" {
			continue
		}

		// Generate centers for each tile in the initial grid
		for i := 0; i < n; i++ {
			centerLat := minLat + (float64(i)+0.5)*stepLat
			for j := 0; j < n; j++ {
				centerLon := minLon + (float64(j)+0.5)*stepLon

				params := &gmaps.MapSearchParams{
					Location: gmaps.MapLocation{
						Lat:     centerLat,
						Lon:     centerLon,
						ZoomLvl: float64(minZoom),
						Radius:  radius,
					},
					Query:          query,
					ViewportW:      600,
					ViewportH:      800,
					Hl:             langCode,
					SplitThreshold: splitThreshold,
					MinZoom:        minZoom,
					MaxZoom:        maxZoom,
					SubdivLevel:    0,
					StaticFirst:    staticFirst,
				}

				opts := []gmaps.SearchJobOptions{}
				if exitMonitor != nil {
					opts = append(opts, gmaps.WithSearchJobExitMonitor(exitMonitor))
				}
				if dedup != nil {
					opts = append(opts, gmaps.WithSearchJobDeduper(dedup))
				}
				// Propagate preflight config into SearchJob so its browser fallback can carry it into Gmap/Place pipeline
				opts = append(opts, gmaps.WithSearchJobPreflight(preflightEnabled, preflightDNSTimeoutMs, preflightTCPTimeoutMs, preflightHEADTimeoutMs, preflightEnableHEAD))

				jobs = append(jobs, gmaps.NewSearchJob(params, opts...))

				// Respect maxTiles cap strictly for initial seeds
				if len(jobs) >= maxTiles {
					break
				}
			}
			if len(jobs) >= maxTiles {
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return jobs, nil
}

// intSqrt returns floor(sqrt(n)) without using math.
func intSqrt(n int) int {
	if n <= 0 {
		return 0
	}
	// Binary search integer sqrt
	low, high := 1, n
	for low <= high {
		mid := (low + high) / 2
		if mid <= n/mid {
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return high
}
