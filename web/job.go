package web

import (
	"context"
	"errors"
	"time"
)

const (
	StatusPending = "pending"
	StatusWorking = "working"
	StatusOK      = "ok"
	StatusFailed  = "failed"
)

type SelectParams struct {
	Status string
	Limit  int
}

type JobRepository interface {
	Get(context.Context, string) (Job, error)
	Create(context.Context, *Job) error
	Delete(context.Context, string) error
	Select(context.Context, SelectParams) ([]Job, error)
	Update(context.Context, *Job) error
	Stats(context.Context) (ServiceStats, error)
}

type Job struct {
	ID     string
	Name   string
	Date   time.Time
	Status string
	Data   JobData
}

func (j *Job) Validate() error {
	if j.ID == "" {
		return errors.New("missing id")
	}

	if j.Name == "" {
		return errors.New("missing name")
	}

	if j.Status == "" {
		return errors.New("missing status")
	}

	if j.Date.IsZero() {
		return errors.New("missing date")
	}

	if err := j.Data.Validate(); err != nil {
		return err
	}

	return nil
}

type JobData struct {
	Keywords []string      `json:"keywords"`
	Lang     string        `json:"lang"`
	Zoom     int           `json:"zoom"`
	Lat      string        `json:"lat"`
	Lon      string        `json:"lon"`
	FastMode bool          `json:"fast_mode"`
	Radius   int           `json:"radius"`
	Depth    int           `json:"depth"`
	Email    bool          `json:"email"`
	MaxTime  time.Duration `json:"max_time"`
	Proxies  []string      `json:"proxies"`

	// Preflight quick checks for website URL (performance-focused)
	EnablePreflight        bool `json:"enable_preflight,omitempty"`
	PreflightDNSTimeoutMs  int  `json:"preflight_dns_timeout_ms,omitempty"`
	PreflightTCPTimeoutMs  int  `json:"preflight_tcp_timeout_ms,omitempty"`
	PreflightHEADTimeoutMs int  `json:"preflight_head_timeout_ms,omitempty"`
	PreflightEnableHEAD    bool `json:"preflight_enable_head,omitempty"`

	// Bounding box for adaptive tiling (city/country scope)
	BboxMinLat string `json:"bbox_min_lat,omitempty"`
	BboxMinLon string `json:"bbox_min_lon,omitempty"`
	BboxMaxLat string `json:"bbox_max_lat,omitempty"`
	BboxMaxLon string `json:"bbox_max_lon,omitempty"`

	// Name-based geocoding to auto-fill bbox
	// provider: "nominatim" (default) or "google"
	// cc: optional ISO 2-letter country code to disambiguate
	// google_api_key: optional API key when provider="google"
	Place        string `json:"place,omitempty"`
	Provider     string `json:"provider,omitempty"`
	CC           string `json:"cc,omitempty"`
	GoogleAPIKey string `json:"google_api_key,omitempty"`

	// Tiling controls
	SplitThreshold int    `json:"split_threshold,omitempty"` // per-tile result threshold to trigger subdivision
	MaxTiles       int    `json:"max_tiles,omitempty"`       // safety cap for tiles per job
	TileSystem     string `json:"tile_system,omitempty"`     // "s2" (default) or "h3"
	StaticFirst    bool   `json:"static_first,omitempty"`    // prefer static pb path first
}

// ServiceStats represents aggregate statistics for the dashboard API.
type ServiceStats struct {
	Total        int       `json:"total"`
	Completed    int       `json:"completed"`
	JobsPerMin   float64   `json:"jobs_per_min"`
	LastActivity time.Time `json:"last_activity"`
}

func (d *JobData) Validate() error {
	if len(d.Keywords) == 0 {
		return errors.New("missing keywords")
	}

	if d.Lang == "" {
		return errors.New("missing lang")
	}

	if len(d.Lang) != 2 {
		return errors.New("invalid lang")
	}

	if d.Depth == 0 {
		return errors.New("missing depth")
	}

	if d.MaxTime == 0 {
		return errors.New("missing max time")
	}

	// Fast mode requires a single lat/lon center
	if d.FastMode && (d.Lat == "" || d.Lon == "") {
		return errors.New("missing geo coordinates")
	}

	// If any bbox field is provided, require all four
	if d.BboxMinLat != "" || d.BboxMinLon != "" || d.BboxMaxLat != "" || d.BboxMaxLon != "" {
		if d.BboxMinLat == "" || d.BboxMinLon == "" || d.BboxMaxLat == "" || d.BboxMaxLon == "" {
			return errors.New("incomplete bbox: require bbox_min_lat, bbox_min_lon, bbox_max_lat, bbox_max_lon")
		}
	}

	return nil
}
