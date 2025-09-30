# Google Maps Scraper

Fast, open-source Google Maps scraping with CLI and Web UI. Extract structured business data at scale with proxy support, REST API, Google Sheets integration, and optional advanced features.

Important
- This repository is a fork/extension of the original by gosom. Some advanced features (e.g., Adaptive Tiling, Email Verification pipeline) are not included in the public Docker image gosom/google-maps-scraper.
- To use these advanced features, build from this repository: https://github.com/sadewadee/google-maps-scraper

## Highlights
- Extract key business data: name, category, address, phone, website, ratings, reviews, coordinates, CID, plus-code, and more
- Export to CSV or JSON; optional PostgreSQL writer
- Web UI and REST API for job management
- Proxy support (SOCKS5/HTTP/HTTPS)
- Google Sheets export/import using a Service Account
- Advanced (fork-only): Adaptive Tiling for city/country-scale coverage, Email Verification pipeline

## Quickstart

Option A — Basic via Docker (public image, core features)
- Uses the upstream image (no tiling / verifier):

```
mkdir -p gmapsdata && docker run -v "$PWD/gmapsdata":/gmapsdata -p 8080:8080 gosom/google-maps-scraper -data-folder /gmapsdata
```

CLI (Docker):

```
touch results.csv && docker run -v "$PWD/example-queries.txt":/example-queries -v "$PWD/results.csv":/results.csv gosom/google-maps-scraper -depth 1 -input /example-queries -results /results.csv -exit-on-inactivity 3m
```

Note: Advanced features like Adaptive Tiling and Email Verification are not available in this image.

Option B — Advanced features (build from this fork)
- Build locally to enable features like Adaptive Tiling and Email Verification.

Local build (Go 1.24.3):

```
git clone https://github.com/sadewadee/google-maps-scraper.git
cd google-maps-scraper
go mod download
go build
./google-maps-scraper -input example-queries.txt -results results.csv -exit-on-inactivity 3m
```

Optional: Build a local Docker image with advanced features:

```
docker build -t google-maps-scraper-local .
docker run --rm -v "$PWD/example-queries.txt":/example-queries -v "$PWD/results.csv":/results.csv -p 8080:8080 google-maps-scraper-local -input /example-queries -results /results.csv
```

## Configuration

Common flags:
- -input: path to query list (one per line)
- -results: output file (CSV by default)
- -json: output JSON instead of CSV
- -depth: search scroll depth (default 10)
- -c: concurrency (default ~ half CPU cores)
- -proxies: comma-separated proxy URLs
- -web: run Web UI

Proxy formats:
- With auth: scheme://username:password@host:port
- Without auth: scheme://host:port
Supported schemes: socks5, socks5h, http, https

Example:

```
./google-maps-scraper -input example-queries.txt -results restaurants.csv -proxies 'http://user:pass@proxy.host:8080' -depth 1 -c 2
```

## Email features

- Email extraction: Collects emails from discovered business websites (availability can depend on the build).
- Email verification pipeline (fork-only): Additional verification jobs (e.g., preflight/verification) are included in this repository. Build from this fork to use them. See source: [gmaps/emailpreflightjob.go](gmaps/emailpreflightjob.go), [gmaps/emailverifyjob.go](gmaps/emailverifyjob.go), [gmaps/emailjob.go](gmaps/emailjob.go).

Note: The public Docker image gosom/google-maps-scraper does not include the verifier pipeline.

## REST API

Key endpoints:
- POST /api/v1/jobs — create a job
- GET /api/v1/jobs — list jobs
- GET /api/v1/jobs/{id} — job details
- DELETE /api/v1/jobs/{id} — delete a job
- GET /api/v1/jobs/{id}/download — download results as CSV

Documentation is served at http://localhost:8080/api/docs (Swagger/Redoc).

## Google Sheets Integration

Export job results to Google Sheets and import keywords from a sheet using a Google Service Account.

Setup:
- Place your service account JSON at keys/credentials.json
- Set env vars:
  - GOOGLE_SHEETS_CREDENTIALS_JSON=/absolute/path/to/keys/credentials.json
  - GOOGLE_SHEETS_DEFAULT_SPREADSHEET_ID=1AbcYourSheetID
  - GOOGLE_SHEETS_DEFAULT_RANGE=Sheet1!A1

References:
- [web.Service.ExportJobToSheets()](web/service.go:89)
- [web.Service.ImportQueriesFromSheet()](web/service.go:162)
- [web.newSheetsService()](web/service.go:240)
- API endpoints:
  - POST /api/v1/jobs/{id}/export/sheets → [web.Server.apiExportSheets()](web/web.go:739)
  - POST /api/v1/import/sheets → [web.Server.apiImportSheets()](web/web.go:778)

Run Web UI with Sheets env:

```
export GOOGLE_SHEETS_CREDENTIALS_JSON="$(pwd)/keys/credentials.json"
export GOOGLE_SHEETS_DEFAULT_SPREADSHEET_ID="1AbcYourSheetID"
export GOOGLE_SHEETS_DEFAULT_RANGE="Sheet1!A1"
go run ./main.go -web -data-folder ./webdata
```

Export example:

```
curl -X POST "http://localhost:8080/api/v1/jobs/{id}/export/sheets" -H "Content-Type: application/json" -d '{"sheet_id":"1AbcYourSheetID","range":"Sheet1!A1","mode":"append"}'
```

Import example:

```
curl -X POST "http://localhost:8080/api/v1/import/sheets" -H "Content-Type: application/json" -d '{"sheet_id":"1AbcYourSheetID","range":"Sheet1!A1:A1000","lang":"en","depth":10,"max_time_seconds":600}'
```

## Adaptive Tiling (Static-first) — fork-only

Maps list results cap at ~120 places per query. Adaptive tiling subdivides geography into viewport-sized tiles, preferring static pb-first requests with browser fallback only when needed. Build from this fork to enable.

Core flow:
- Static-first search; browser fallback on redirect/parse failure → [SearchJob.Process()](gmaps/searchjob.go:86)
- Cross-tile deduplication via canonical keys → [BuildEntryKey()](gmaps/entry.go:709)

Flags (available in fork build):
- -split-threshold, -max-tiles, -tile-system, -static-first → [runner.ParseConfig()](runner/runner.go:97)

Web UI fields (fork build):
- bbox_min_lat, bbox_min_lon, bbox_max_lat, bbox_max_lon → [Server.index() defaults](web/web.go:243)
- split_threshold, max_tiles, tile_system, static_first → [Server.scrape()](web/web.go:357)

API example (POST /api/v1/jobs):

```json
{
  "name": "Jakarta Food Tiling",
  "jobdata": {
    "keywords": ["rumah makan jakarta"],
    "lang": "id",
    "zoom": 12,
    "fast_mode": true,
    "radius": 10000,
    "depth": 10,
    "email": false,
    "bbox_min_lat": "-6.40",
    "bbox_min_lon": "106.70",
    "bbox_max_lat": "-6.10",
    "bbox_max_lon": "106.95",
    "split_threshold": 90,
    "max_tiles": 250000,
    "tile_system": "s2",
    "static_first": true
  }
}
```

Example commands (fork build):

- Local binary:
  - ./google-maps-scraper -web -data-folder ./webdata -split-threshold 90 -max-tiles 250000 -tile-system s2 -static-first
- Local Docker image:
  - docker run -p 8080:8080 google-maps-scraper-local -data-folder /gmapsdata -split-threshold 90 -max-tiles 250000 -tile-system s2 -static-first

## Output

CSV or JSON includes core fields such as input_id, link, title, category, address, phone, website, review_count, review_rating, latitude, longitude, cid, and optionally emails. Use -json if you need richer review data or extended fields.

## Performance

Typical throughput with -c 8 and -depth 1 is ~120 jobs/minute. For large keyword sets, consider the database provider and running multiple workers (e.g., Kubernetes).

## Telemetry

Anonymous usage statistics help debugging and improvements. Opt out with DISABLE_TELEMETRY=1.

## Notes

Use responsibly and comply with applicable laws and target site terms.

## License

MIT License. See LICENSE.

## Credits

Based on and credit to [gosom](https://github.com/gosom/google-maps-scraper). Created and maintained by the fork author with enhancements. If you find the project useful, please star the original repository and consider sponsoring gosom.
