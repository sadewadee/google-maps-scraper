package web

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

    // Remove CSV or JSON result files if they exist
    csvPath := filepath.Join(s.dataFolder, id+".csv")
    jsonPath := filepath.Join(s.dataFolder, id+".json")

    if _, err := os.Stat(csvPath); err == nil {
        if err := os.Remove(csvPath); err != nil {
            return err
        }
    } else if !os.IsNotExist(err) {
        return err
    }

    if _, err := os.Stat(jsonPath); err == nil {
        if err := os.Remove(jsonPath); err != nil {
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

    // Prefer CSV, but fall back to JSON if CSV does not exist
    csvPath := filepath.Join(s.dataFolder, id+".csv")
    if _, err := os.Stat(csvPath); err == nil {
        return csvPath, nil
    }

    jsonPath := filepath.Join(s.dataFolder, id+".json")
    if _, err := os.Stat(jsonPath); err == nil {
        return jsonPath, nil
    }

    return "", fmt.Errorf("result file not found for job %s", id)
}
