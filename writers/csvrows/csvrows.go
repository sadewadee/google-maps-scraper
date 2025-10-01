package csvrows

import (
	"context"
	"encoding/csv"
	"errors"

	"github.com/gosom/scrapemate"
	"github.com/sadewadee/google-maps-scraper/gmaps"
)

// Writer implements scrapemate.ResultWriter and writes both single entries (*gmaps.Entry)
// and batches ([]*gmaps.Entry) to CSV, always emitting headers once per stream.
type Writer struct {
	cw          *csv.Writer
	wroteHeader bool
}

// New constructs a CSV rows writer that understands both *gmaps.Entry and []*gmaps.Entry.
func New(cw *csv.Writer) scrapemate.ResultWriter {
	return &Writer{cw: cw}
}

func (w *Writer) Run(ctx context.Context, in <-chan scrapemate.Result) error {
	defer w.cw.Flush()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case res, ok := <-in:
			if !ok {
				return nil
			}

			entries, err := toEntries(res.Data)
			if err != nil {
				// Be strict to surface pipeline type mismatches
				return err
			}
			if len(entries) == 0 {
				continue
			}

			// Write headers once
			if !w.wroteHeader {
				if err := w.cw.Write(entries[0].CsvHeaders()); err != nil {
					return err
				}
				w.wroteHeader = true
			}

			for _, e := range entries {
				if e == nil {
					continue
				}
				if err := w.cw.Write(e.CsvRow()); err != nil {
					return err
				}
			}
		}
	}
}

func toEntries(data any) ([]*gmaps.Entry, error) {
	switch v := data.(type) {
	case *gmaps.Entry:
		if v == nil {
			return nil, nil
		}
		return []*gmaps.Entry{v}, nil
	case []*gmaps.Entry:
		return v, nil
	default:
		return nil, errors.New("csvrows: unsupported data type (expected *gmaps.Entry or []*gmaps.Entry)")
	}
}
