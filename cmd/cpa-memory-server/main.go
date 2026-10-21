// cpa-memory-server runs recall independently from the archive collector.
package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"cpa-session-archive/internal/archivefeed"
	"cpa-session-archive/internal/embedding"
	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/projectionlock"
	"cpa-session-archive/internal/recallruntime"
)

type config struct {
	database, archiveEndpoint, archiveTokenFile                 string
	adoptSource                                                 bool
	runtime                                                     recallruntime.Config
	embeddingEndpoint, embeddingTokenFile, embeddingFingerprint string
	embeddingDimensions                                         int
	semanticMinimum                                             float64
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
	cfg.embeddingEndpoint = getenv("MEMORY_EMBEDDING_ENDPOINT")
	cfg.embeddingTokenFile = getenv("MEMORY_EMBEDDING_TOKEN_FILE")
	cfg.embeddingFingerprint = getenv("MEMORY_EMBEDDING_FINGERPRINT")
	dimensions := getenv("MEMORY_EMBEDDING_DIMENSIONS")
	minimum := getenv("MEMORY_SEMANTIC_MINIMUM")
	if cfg.embeddingEndpoint == "" {
		if cfg.embeddingTokenFile != "" || cfg.embeddingFingerprint != "" || dimensions != "" || minimum != "" {
			return cfg, fmt.Errorf("embedding settings require MEMORY_EMBEDDING_ENDPOINT")
		}
		return cfg, nil
	}
	if cfg.embeddingTokenFile == "" || cfg.embeddingFingerprint == "" {
		return cfg, fmt.Errorf("local embeddings require a dedicated token file and pinned fingerprint")
	}
	var err error
	cfg.embeddingDimensions, err = strconv.Atoi(dimensions)
	if err != nil || cfg.embeddingDimensions < 1 || cfg.embeddingDimensions > 4096 {
		return cfg, fmt.Errorf("MEMORY_EMBEDDING_DIMENSIONS must be between 1 and 4096")
	}
	cfg.semanticMinimum = 0.8
	if minimum != "" {
		cfg.semanticMinimum, err = strconv.ParseFloat(minimum, 64)
		if err != nil || math.IsNaN(cfg.semanticMinimum) || math.IsInf(cfg.semanticMinimum, 0) || cfg.semanticMinimum < 0 || cfg.semanticMinimum > 1 {
			return cfg, fmt.Errorf("MEMORY_SEMANTIC_MINIMUM must be between 0 and 1")
		}
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
	var embedder *embedding.HTTPClient
	if cfg.embeddingEndpoint != "" {
		embeddingToken, err := recallruntime.ReadTokenFile(cfg.embeddingTokenFile)
		if err != nil {
			return fmt.Errorf("local embedding credential: %w", err)
		}
		if embeddingToken == token {
			return fmt.Errorf("archive feed and embedding credentials must be distinct")
		}
		embedder, err = embedding.NewHTTP(cfg.embeddingEndpoint, embeddingToken, cfg.embeddingFingerprint, cfg.embeddingDimensions)
		if err != nil {
			return err
		}
		defer embedder.Close()
		cfg.runtime.ReservedTokens = append(cfg.runtime.ReservedTokens, embeddingToken)
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
	if embedder != nil {
		options = append(options, memory.WithSemantic(embedder, cfg.semanticMinimum))
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
