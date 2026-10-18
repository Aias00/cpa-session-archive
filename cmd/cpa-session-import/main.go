// cpa-session-import merges an immutable source snapshot without overwriting
// destination requests. Run only after taking an online destination backup.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"cpa-session-archive/internal/archive"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "archive import failed:", err)
		os.Exit(1)
	}
}
func run() error {
	source := flag.String("source", "", "immutable source SQLite snapshot")
	target := flag.String("target", "", "existing destination SQLite archive")
	reportPath := flag.String("report", "", "new private JSON report path")
	batch := flag.Int("batch", 8, "records committed per batch (1-64)")
	flag.Parse()
	if *source == "" || *target == "" || *reportPath == "" {
		return fmt.Errorf("source, target and report are required")
	}
	file, err := os.OpenFile(*reportPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	absolute, err := filepath.Abs(*target)
	if err != nil {
		return err
	}
	uri := (&url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=rw&_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=on"}).String()
	db, err := sql.Open("sqlite3", uri)
	if err != nil {
		return err
	}
	defer db.Close()
	store := &archive.Store{DB: db, DBPath: absolute, StoreUpstream: true}
	started := time.Now()
	report, importErr := store.ImportSnapshot(context.Background(), *source, *batch)
	result := map[string]any{"source": *source, "target": absolute, "started_at": started.UTC().Format(time.RFC3339Nano), "elapsed_seconds": time.Since(started).Seconds(), "complete": importErr == nil && report.Remaining == 0, "result": report}
	if importErr != nil {
		result["error"] = importErr.Error()
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if importErr != nil {
		return importErr
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"source_records": report.SourceRecords, "imported": report.Imported, "existing": report.Existing, "conflicts_preserved_on_target": report.Conflicts, "copied_blobs": report.CopiedBlobs, "remaining": report.Remaining, "elapsed_seconds": time.Since(started).Seconds()})
}
