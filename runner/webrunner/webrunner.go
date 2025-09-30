package webrunner

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sadewadee/google-maps-scraper/deduper"
	"github.com/sadewadee/google-maps-scraper/exiter"
	"github.com/sadewadee/google-maps-scraper/runner"
	"github.com/sadewadee/google-maps-scraper/tlmt"
	"github.com/sadewadee/google-maps-scraper/web"
	"github.com/sadewadee/google-maps-scraper/web/sqlite"
	"github.com/gosom/scrapemate"
	"github.com/gosom/scrapemate/adapters/writers/csvwriter"
	"github.com/gosom/scrapemate/scrapemateapp"
	"golang.org/x/sync/errgroup"
)

type webrunner struct {
	srv   *web.Server
	svc   *web.Service
	cfg   *runner.Config
	dedup deduper.Deduper
}

func New(cfg *runner.Config) (runner.Runner, error) {
	if cfg.DataFolder == "" {
		return nil, fmt.Errorf("data folder is required")
	}

	if err := os.MkdirAll(cfg.DataFolder, os.ModePerm); err != nil {
		return nil, err
	}

	const dbfname = "jobs.db"

	dbpath := filepath.Join(cfg.DataFolder, dbfname)

	repo, err := sqlite.New(dbpath)
	if err != nil {
		return nil, err
	}

	svc := web.NewService(repo, cfg.DataFolder)

	srv, err := web.New(svc, cfg.Addr)
	if err != nil {
		return nil, err
	}

	// initialize deduper (persistent across jobs when enabled)
	var dd deduper.Deduper
	if cfg.PersistentDedup {
		if d, err := deduper.NewPersistentSQLite(dbpath); err == nil {
			dd = d
		} else {
			log.Printf("persistent deduper init failed (%v), falling back to in-memory", err)
			dd = deduper.New()
		}
	} else {
		dd = deduper.New()
	}

	ans := webrunner{
		srv:   srv,
		svc:   svc,
		cfg:   cfg,
		dedup: dd,
	}

	return &ans, nil
}

func (w *webrunner) Run(ctx context.Context) error {
	egroup, ctx := errgroup.WithContext(ctx)

	egroup.Go(func() error {
		return w.work(ctx)
	})

	egroup.Go(func() error {
		return w.srv.Start(ctx)
	})

	return egroup.Wait()
}

func (w *webrunner) Close(context.Context) error {
	return nil
}

func (w *webrunner) work(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			jobs, err := w.svc.SelectPending(ctx)
			if err != nil {
				return err
			}

			for i := range jobs {
				select {
				case <-ctx.Done():
					return nil
				default:
					t0 := time.Now().UTC()
					if err := w.scrapeJob(ctx, &jobs[i]); err != nil {
						params := map[string]any{
							"job_count": len(jobs[i].Data.Keywords),
							"duration":  time.Now().UTC().Sub(t0).String(),
							"error":     err.Error(),
						}

						evt := tlmt.NewEvent("web_runner", params)

						_ = runner.Telemetry().Send(ctx, evt)

						log.Printf("error scraping job %s: %v", jobs[i].ID, err)
					} else {
						params := map[string]any{
							"job_count": len(jobs[i].Data.Keywords),
							"duration":  time.Now().UTC().Sub(t0).String(),
						}

						_ = runner.Telemetry().Send(ctx, tlmt.NewEvent("web_runner", params))

						log.Printf("job %s scraped successfully", jobs[i].ID)
					}
				}
			}
		}
	}
}

func (w *webrunner) scrapeJob(ctx context.Context, job *web.Job) error {
	var err error
	job.Status = web.StatusWorking

	err = w.svc.Update(ctx, job)
	if err != nil {
		return err
	}

	if len(job.Data.Keywords) == 0 {
		job.Status = web.StatusFailed

		return w.svc.Update(ctx, job)
	}

	outpath := filepath.Join(w.cfg.DataFolder, job.ID+".csv")

	outfile, err := os.Create(outpath)
	if err != nil {
		return err
	}

	defer func() {
		_ = outfile.Close()
	}()

	mate, err := w.setupMate(ctx, outfile, job)
	if err != nil {
		job.Status = web.StatusFailed

		err2 := w.svc.Update(ctx, job)
		if err2 != nil {
			log.Printf("failed to update job status: %v", err2)
		}

		return err
	}

	defer mate.Close()

	var coords string
	if job.Data.Lat != "" && job.Data.Lon != "" {
		coords = job.Data.Lat + "," + job.Data.Lon
	}

	// using runner-level deduper (persistent if enabled)
	exitMonitor := exiter.New()

	var seedJobs []scrapemate.IJob

	// If bbox provided, use adaptive tiling with static pb-first seeds
	hasBBox := job.Data.BboxMinLat != "" && job.Data.BboxMinLon != "" && job.Data.BboxMaxLat != "" && job.Data.BboxMaxLon != ""
	if hasBBox {
		// Use job-specified tiling settings, falling back to runner config defaults
		splitThreshold := job.Data.SplitThreshold
		if splitThreshold <= 0 {
			if w.cfg.SplitThreshold > 0 {
				splitThreshold = w.cfg.SplitThreshold
			} else {
				splitThreshold = 90
			}
		}
		maxTiles := job.Data.MaxTiles
		if maxTiles <= 0 {
			if w.cfg.MaxTiles > 0 {
				maxTiles = w.cfg.MaxTiles
			} else {
				maxTiles = 250000
			}
		}
		staticFirst := job.Data.StaticFirst
		if !staticFirst {
			staticFirst = w.cfg.StaticFirst
		}
		minZoom := job.Data.Zoom
		if minZoom < 1 {
			minZoom = 10
		}
		maxZoom := minZoom + 3
		if maxZoom > 21 {
			maxZoom = 21
		}
		rad := func() float64 {
			if job.Data.Radius <= 0 {
				return 10000 // 10 km
			}
			return float64(job.Data.Radius)
		}()

		seedJobs, err = runner.CreateTiledSeedJobs(
			job.Data.Lang,
			strings.NewReader(strings.Join(job.Data.Keywords, "\n")),
			job.Data.BboxMinLat,
			job.Data.BboxMinLon,
			job.Data.BboxMaxLat,
			job.Data.BboxMaxLon,
			minZoom,
			maxZoom,
			splitThreshold,
			maxTiles,
			staticFirst,
			rad,
			w.dedup,
			exitMonitor,
		)
	} else {
		seedJobs, err = runner.CreateSeedJobs(
			job.Data.FastMode,
			job.Data.Lang,
			strings.NewReader(strings.Join(job.Data.Keywords, "\n")),
			job.Data.Depth,
			job.Data.Email,
			coords,
			job.Data.Zoom,
			func() float64 {
				if job.Data.Radius <= 0 {
					return 10000 // 10 km
				}

				return float64(job.Data.Radius)
			}(),
			w.dedup,
			exitMonitor,
			w.cfg.ExtraReviews,
		)
	}
	if err != nil {
		err2 := w.svc.Update(ctx, job)
		if err2 != nil {
			log.Printf("failed to update job status: %v", err2)
		}

		return err
	}

	if len(seedJobs) > 0 {
		exitMonitor.SetSeedCount(len(seedJobs))

		// Per-query timeout instead of per-job
		perQuerySeconds := max(60, 10*job.Data.Depth/50+120)

		if job.Data.MaxTime > 0 {
			if job.Data.MaxTime.Seconds() < 180 {
				perQuerySeconds = 180
			} else {
				perQuerySeconds = int(job.Data.MaxTime.Seconds())
			}
		}

		for i := range seedJobs {
			log.Printf("running job %s seed %d/%d with %d allowed seconds", job.ID, i+1, len(seedJobs), perQuerySeconds)

			mateCtx, cancel := context.WithTimeout(ctx, time.Duration(perQuerySeconds)*time.Second)

			exitMonitor.SetCancelFunc(cancel)
			go exitMonitor.Run(mateCtx)

			err = mate.Start(mateCtx, seedJobs[i])
			if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
				cancel()

				err2 := w.svc.Update(ctx, job)
				if err2 != nil {
					log.Printf("failed to update job status: %v", err2)
				}

				return err
			}

			cancel()
		}
	}

	mate.Close()

	job.Status = web.StatusOK

	return w.svc.Update(ctx, job)
}

func (w *webrunner) setupMate(_ context.Context, writer io.Writer, job *web.Job) (*scrapemateapp.ScrapemateApp, error) {
	opts := []func(*scrapemateapp.Config) error{
		scrapemateapp.WithConcurrency(w.cfg.Concurrency),
		scrapemateapp.WithExitOnInactivity(time.Minute * 3),
	}

	if !job.Data.FastMode {
		opts = append(opts,
			scrapemateapp.WithJS(scrapemateapp.DisableImages()),
		)
	} else {
		opts = append(opts,
			scrapemateapp.WithStealth("firefox"),
		)
	}

	hasProxy := false

	if len(w.cfg.Proxies) > 0 {
		opts = append(opts, scrapemateapp.WithProxies(w.cfg.Proxies))
		hasProxy = true
	} else if len(job.Data.Proxies) > 0 {
		opts = append(opts,
			scrapemateapp.WithProxies(job.Data.Proxies),
		)
		hasProxy = true
	}

	if !w.cfg.DisablePageReuse {
		opts = append(opts,
			scrapemateapp.WithPageReuseLimit(2),
			scrapemateapp.WithPageReuseLimit(200),
		)
	}

	log.Printf("job %s has proxy: %v", job.ID, hasProxy)

	csvWriter := csvwriter.NewCsvWriter(csv.NewWriter(writer))

	writers := []scrapemate.ResultWriter{csvWriter}

	matecfg, err := scrapemateapp.NewConfig(
		writers,
		opts...,
	)
	if err != nil {
		return nil, err
	}

	return scrapemateapp.NewScrapeMateApp(matecfg)
}
