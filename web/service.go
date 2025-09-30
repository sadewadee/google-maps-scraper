package web

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

type Service struct {
	repo       JobRepository
	dataFolder string
}

func NewService(repo JobRepository, dataFolder string) *Service {
	return &Service{
		repo:       repo,
		dataFolder: dataFolder,
	}
}

func (s *Service) Create(ctx context.Context, job *Job) error {
	if job.Name == "" && len(job.Data.Keywords) > 0 {
		job.Name = job.Data.Keywords[0]
	}
	return s.repo.Create(ctx, job)
}

func (s *Service) All(ctx context.Context) ([]Job, error) {
	return s.repo.Select(ctx, SelectParams{})
}

func (s *Service) Get(ctx context.Context, id string) (Job, error) {
	return s.repo.Get(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return fmt.Errorf("invalid file name")
	}

	datapath := filepath.Join(s.dataFolder, id+".csv")

	if _, err := os.Stat(datapath); err == nil {
		if err := os.Remove(datapath); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	return s.repo.Delete(ctx, id)
}

func (s *Service) Update(ctx context.Context, job *Job) error {
	return s.repo.Update(ctx, job)
}

func (s *Service) SelectPending(ctx context.Context) ([]Job, error) {
	return s.repo.Select(ctx, SelectParams{Status: StatusPending, Limit: 1})
}

func (s *Service) GetCSV(_ context.Context, id string) (string, error) {
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid file name")
	}

	datapath := filepath.Join(s.dataFolder, id+".csv")

	if _, err := os.Stat(datapath); os.IsNotExist(err) {
		return "", fmt.Errorf("csv file not found for job %s", id)
	}

	return datapath, nil
}

func (s *Service) Stats(ctx context.Context) (ServiceStats, error) {
	return s.repo.Stats(ctx)
}

// ExportJobToSheets exports the CSV results of a job to Google Sheets.
// sheetID: target spreadsheet ID (uses GOOGLE_SHEETS_DEFAULT_SPREADSHEET_ID when empty)
// rng: target range like "Sheet1!A1" (uses GOOGLE_SHEETS_DEFAULT_RANGE or defaults to "Sheet1!A1" when empty)
// appendMode: when true uses Append (INSERT_ROWS); when false uses Update (overwrite)
func (s *Service) ExportJobToSheets(ctx context.Context, jobID, sheetID, rng string, appendMode bool) error {
	csvPath, err := s.GetCSV(ctx, jobID)
	if err != nil {
		return err
	}

	f, err := os.Open(csvPath)
	if err != nil {
		return fmt.Errorf("open csv: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1

	var values [][]interface{}
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read csv: %w", err)
		}
		row := make([]interface{}, len(rec))
		for i, col := range rec {
			row[i] = col
		}
		values = append(values, row)
	}

	if sheetID == "" {
		sheetID = os.Getenv("GOOGLE_SHEETS_DEFAULT_SPREADSHEET_ID")
	}
	if sheetID == "" {
		return fmt.Errorf("missing sheetID")
	}
	if rng == "" {
		rng = os.Getenv("GOOGLE_SHEETS_DEFAULT_RANGE")
		if rng == "" {
			rng = "Sheet1!A1"
		}
	}

	srv, err := newSheetsService(ctx)
	if err != nil {
		return err
	}

	vr := &sheets.ValueRange{Values: values}
	if appendMode {
		_, err = srv.Spreadsheets.Values.Append(sheetID, rng, vr).
			ValueInputOption("RAW").
			InsertDataOption("INSERT_ROWS").
			Context(ctx).Do()
	} else {
		_, err = srv.Spreadsheets.Values.Update(sheetID, rng, vr).
			ValueInputOption("RAW").
			Context(ctx).Do()
	}
	if err != nil {
		return fmt.Errorf("sheets write: %w", err)
	}

	return nil
}

// ImportQueriesFromSheet reads keywords from a Google Sheet and creates pending jobs.
// Each non-empty cell in the first column becomes a job with a single keyword.
func (s *Service) ImportQueriesFromSheet(ctx context.Context, sheetID, rng, lang string, depth int, maxTime time.Duration) ([]Job, error) {
	if sheetID == "" {
		sheetID = os.Getenv("GOOGLE_SHEETS_DEFAULT_SPREADSHEET_ID")
	}
	if sheetID == "" {
		return nil, fmt.Errorf("missing sheetID")
	}
	if rng == "" {
		rng = os.Getenv("GOOGLE_SHEETS_DEFAULT_RANGE")
		if rng == "" {
			rng = "Sheet1!A1"
		}
	}
	if lang == "" {
		lang = "en"
	}
	if depth <= 0 {
		depth = 10
	}
	if maxTime <= 0 {
		maxTime = 10 * time.Minute
	}

	srv, err := newSheetsService(ctx)
	if err != nil {
		return nil, err
	}

	res, err := srv.Spreadsheets.Values.Get(sheetID, rng).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("sheets read: %w", err)
	}

	var created []Job
	for _, row := range res.Values {
		if len(row) == 0 {
			continue
		}
		kw, _ := row[0].(string)
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}

		job := Job{
			ID:     uuid.New().String(),
			Name:   kw,
			Date:   time.Now().UTC(),
			Status: StatusPending,
			Data: JobData{
				Keywords: []string{kw},
				Lang:     lang,
				Zoom:     15,
				Lat:      "",
				Lon:      "",
				FastMode: false,
				Radius:   10000,
				Depth:    depth,
				Email:    false,
				MaxTime:  maxTime,
			},
		}

		if err := job.Validate(); err != nil {
			// Skip invalid rows (e.g., malformed lang), continue importing others
			continue
		}
		if err := s.repo.Create(ctx, &job); err != nil {
			return nil, err
		}
		created = append(created, job)
	}

	return created, nil
}

// newSheetsService initializes the Google Sheets client using service account credentials.
// Requires GOOGLE_SHEETS_CREDENTIALS_JSON to point to a credentials.json file.
func newSheetsService(ctx context.Context) (*sheets.Service, error) {
	credPath := os.Getenv("GOOGLE_SHEETS_CREDENTIALS_JSON")
	if credPath == "" {
		// Fallback to default location if env var not set
		defaultPath := filepath.Join("keys", "credentials.json")
		if _, err := os.Stat(defaultPath); err == nil {
			credPath = defaultPath
		} else {
			return nil, fmt.Errorf("missing GOOGLE_SHEETS_CREDENTIALS_JSON")
		}
	}
	b, err := os.ReadFile(credPath)
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	srv, err := sheets.NewService(ctx,
		option.WithCredentialsJSON(b),
		option.WithScopes(sheets.SpreadsheetsScope),
	)
	if err != nil {
		return nil, fmt.Errorf("sheets service: %w", err)
	}
	return srv, nil
}
