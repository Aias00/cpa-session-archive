// cpa-history-sync reads client history files into a private staging archive.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/historysync"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "native history sync:", err)
		os.Exit(1)
	}
}
func run() error {
	homeDefault, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	home := flag.String("home", homeDefault, "client history home directory")
	device := flag.String("device", "", "stable source device ID (required)")
	target := flag.String("archive", "", "private staging SQLite archive (required)")
	codexHome := flag.String("codex-home", "", "explicit absolute Codex home")
	claudeHome := flag.String("claude-home", "", "explicit absolute Claude Code home")
	clients := flag.String("clients", "", "optional comma-separated native client IDs to collect")
	verifyAll := flag.Bool("verify-all", false, "ignore unchanged-file proofs and verify every committed prefix")
	reportPath := flag.String("report", "", "new private JSON report path")
	exportPath := flag.String("export", "", "export a new delta archive instead of collecting")
	after := flag.Int64("after", 0, "exclusive staging event watermark for export")
	through := flag.Int64("through", 0, "inclusive staging event watermark for export")
	flag.Parse()
	if *device == "" || *target == "" {
		return fmt.Errorf("device and archive are required")
	}
	if err := os.MkdirAll(filepath.Dir(*target), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(*target+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another history synchronization is active")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	file, err := os.OpenFile(*target, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	file.Close()
	store, err := archive.OpenStore(*target, false)
	if err != nil {
		return err
	}
	defer store.DB.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *exportPath != "" {
		return exportCommitted(ctx, store, *exportPath, *after, *through, os.Stdout)
	}
	started := time.Now()
	last := started
	roots, err := historysync.DefaultRoots(*home)
	if err != nil {
		return err
	}
	if *codexHome != "" {
		if !filepath.IsAbs(*codexHome) {
			return fmt.Errorf("codex-home must be absolute")
		}
		roots[0].Path = filepath.Join(*codexHome, "sessions")
		roots[1].Path = filepath.Join(*codexHome, "archived_sessions")
	}
	if *claudeHome != "" {
		if !filepath.IsAbs(*claudeHome) {
			return fmt.Errorf("claude-home must be absolute")
		}
		roots[2].Path = filepath.Join(*claudeHome, "projects")
	}

	if *clients != "" {
		selected := map[string]bool{}
		for _, id := range strings.Split(*clients, ",") {
			id = strings.TrimSpace(id)
			found := false
			for _, root := range roots {
				if root.Client == id {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("unknown native client filter")
			}
			selected[id] = true
		}
		filtered := roots[:0]
		for _, root := range roots {
			if selected[root.Client] {
				filtered = append(filtered, root)
			}
		}
		roots = filtered
	}
	report, syncErr := historysync.Sync(ctx, historysync.Config{Store: store, Device: *device, Roots: roots, VerifyAll: *verifyAll, Progress: func(r historysync.Report) {
		if time.Since(last) >= 15*time.Second {
			fmt.Fprintf(os.Stderr, "native sync: files=%d new_segments=%d bytes=%d failures=%d\n", r.Files, r.Segments, r.Bytes, r.Failures)
			last = time.Now()
		}
	}})
	result := struct {
		Complete bool               `json:"complete"`
		Elapsed  float64            `json:"elapsed_seconds"`
		Result   historysync.Report `json:"result"`
	}{syncErr == nil && report.Failures == 0, time.Since(started).Seconds(), report}
	if *reportPath != "" {
		file, err := os.OpenFile(*reportPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(file).Encode(result); err != nil {
			file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			file.Close()
			return err
		}
		file.Close()
	}
	// Public progress contains only aggregate metadata; private reports hold paths.
	summary := map[string]any{"complete": result.Complete, "elapsed_seconds": result.Elapsed, "unchanged_skipped": report.UnchangedSkipped, "files": report.Files, "segments": report.Segments, "existing_segments": report.Existing, "bytes": report.Bytes, "pending_tails": report.PendingTails, "failures": report.Failures}
	if err := json.NewEncoder(os.Stdout).Encode(summary); err != nil {
		return err
	}
	if syncErr != nil {
		return syncErr
	}
	if report.Failures > 0 {
		return fmt.Errorf("%d source files require reconciliation; consult private report", report.Failures)
	}
	return nil
}
