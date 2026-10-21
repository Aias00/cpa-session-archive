// cpa-memory-server runs recall independently from the archive collector.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"cpa-session-archive/internal/archivefeed"
	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/projectionlock"
	"cpa-session-archive/internal/recallruntime"
)

type config struct {
	database, archiveEndpoint, archiveTokenFile string
	adoptSource                                 bool
	runtime                                     recallruntime.Config
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv); err != nil {
		log.Printf("memory server stopped: %v", err)
		os.Exit(1)
	}
}

func loadConfig(getenv func(string) string) (config, error) {
	var cfg config
	adoption := getenv("MEMORY_ADOPT_ARCHIVE_SOURCE")
	if adoption != "" && adoption != "true" && adoption != "false" {
		return cfg, fmt.Errorf("MEMORY_ADOPT_ARCHIVE_SOURCE must be true or false")
	}
	cfg.adoptSource = adoption == "true"
	cfg.database = getenv("MEMORY_DB")
	if cfg.database == "" || !filepath.IsAbs(cfg.database) {
		return cfg, fmt.Errorf("MEMORY_DB must be an absolute projection database path")
	}
	if info, err := os.Lstat(cfg.database); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return cfg, fmt.Errorf("MEMORY_DB must be a private regular file (0600)")
		}
	} else if !os.IsNotExist(err) {
		return cfg, fmt.Errorf("cannot inspect MEMORY_DB")
	}
	cfg.archiveEndpoint = getenv("MEMORY_ARCHIVE_ENDPOINT")
	if cfg.archiveEndpoint == "" {
		cfg.archiveEndpoint = "http://127.0.0.1:8318"
	}
	cfg.archiveTokenFile = getenv("MEMORY_ARCHIVE_TOKEN_FILE")
	if cfg.archiveTokenFile == "" {
		return cfg, fmt.Errorf("MEMORY_ARCHIVE_TOKEN_FILE is required")
	}
	cfg.runtime = recallruntime.Config{Listen: getenv("MEMORY_LISTEN_ADDR"), TokenFile: getenv("MEMORY_READ_TOKEN_FILE"), CredentialsFile: getenv("MEMORY_READ_CREDENTIALS_FILE")}
	if cfg.runtime.Listen == "" {
		cfg.runtime.Listen = "127.0.0.1:8319"
	}
	if cfg.runtime.TokenFile == "" {
		return cfg, fmt.Errorf("MEMORY_READ_TOKEN_FILE is required")
	}
	return cfg, nil
}

func run(ctx context.Context, getenv func(string) string) error {
	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	token, err := recallruntime.ReadTokenFile(cfg.archiveTokenFile)
	if err != nil {
		return fmt.Errorf("archive feed credential: %w", err)
	}
	feed, err := archivefeed.NewClient(cfg.archiveEndpoint, token)
	if err != nil {
		return err
	}
	defer feed.Close()
	lock, err := projectionlock.Acquire(cfg.database)
	if err != nil {
		return err
	}
	defer lock.Close()
	var options []memory.OpenOption
	if cfg.adoptSource {
		options = append(options, memory.WithSourceAdoption())
	}
	projection, err := memory.Open(cfg.database, feed, options...)
	if err != nil {
		return fmt.Errorf("open memory projection: %w", err)
	}
	cfg.runtime.Diagnostics = feed.IngestionStatus
	cfg.runtime.ReservedToken = token
	service, err := recallruntime.Start(ctx, projection, cfg.runtime)
	if err != nil {
		_ = projection.Close()
		return fmt.Errorf("start recall: %w", err)
	}
	defer service.Close()
	log.Printf("memory recall listening on %s", service.Addr())
	<-service.Done()
	return service.Err()
}
