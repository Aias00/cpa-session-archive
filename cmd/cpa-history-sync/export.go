package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"cpa-session-archive/internal/archive"
)

// The caller holds the staging lock until all selected CAS dependencies are
// copied. Subsequent syncs transfer small independent deltas instead of writing
// another full staging snapshot on every timer tick.
func exportCommitted(ctx context.Context, source *archive.Store, path string, after, through int64, output io.Writer) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	file.Close()
	target, err := archive.OpenStore(path, false)
	if err != nil {
		return err
	}
	report, err := target.ImportSnapshotRange(ctx, source.DBPath, 8, after, through)
	if err == nil && (report.Remaining != 0 || report.Conflicts != 0) {
		err = fmt.Errorf("delta export did not commit every selected source")
	}
	closeErr := target.DB.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return json.NewEncoder(output).Encode(map[string]any{"exported": report.Imported, "after": after, "through": through})
}
