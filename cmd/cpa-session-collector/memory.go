package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/storage"
)

type memoryConfig struct{ DB, Listen, TokenFile, CredentialsFile string }
type memoryService struct {
	engine     memory.Projection
	listener   net.Listener
	server     *http.Server
	cancel     context.CancelFunc
	workerDone chan struct{}
	once       sync.Once
}

func (s *memoryService) Close() {
	s.once.Do(func() { s.cancel(); s.server.Close(); <-s.workerDone; s.engine.Close() })
}

func startMemory(parent context.Context, feed storage.MemoryFeed, archivePath string, ready <-chan struct{}, cfg memoryConfig) (*memoryService, error) {
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("invalid memory listen address: %w", err)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("memory listener must bind a loopback address")
		}
	}
	archiveAbs, err := filepath.Abs(archivePath)
	if err != nil {
		return nil, err
	}
	memoryAbs, err := filepath.Abs(cfg.DB)
	if err != nil {
		return nil, err
	}
	archiveInfo, _ := os.Stat(archiveAbs)
	memoryInfo, _ := os.Stat(memoryAbs)
	if archiveAbs == memoryAbs || (archiveInfo != nil && memoryInfo != nil && os.SameFile(archiveInfo, memoryInfo)) {
		return nil, fmt.Errorf("memory database must be separate from archive database")
	}
	if cfg.TokenFile == "" {
		return nil, fmt.Errorf("MEMORY_READ_TOKEN_FILE is required")
	}
	f, err := os.Open(cfg.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("open memory read token: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("read memory token: %w", err)
	}
	if len(data) > 4096 {
		return nil, fmt.Errorf("memory token file is too large")
	}
	token := strings.TrimSpace(string(data))
	engine, err := memory.Open(cfg.DB, feed)
	if err != nil {
		return nil, fmt.Errorf("open memory projection: %w", err)
	}
	credentials := []memory.Credential{{Token: token, Scope: memory.Scope{AllowAll: true}}}
	if cfg.CredentialsFile != "" {
		file, err := os.Open(cfg.CredentialsFile)
		if err != nil {
			engine.Close()
			return nil, fmt.Errorf("open additional recall credentials: %w", err)
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			file.Close()
			engine.Close()
			return nil, fmt.Errorf("additional recall credentials must be a private regular file (0600)")
		}
		raw, err := io.ReadAll(io.LimitReader(file, 65537))
		file.Close()
		if err != nil || len(raw) > 65536 {
			engine.Close()
			return nil, fmt.Errorf("invalid recall credentials file")
		}
		var extra []memory.Credential
		if err := json.Unmarshal(raw, &extra); err != nil {
			engine.Close()
			return nil, fmt.Errorf("invalid recall credentials JSON")
		}
		credentials = append(credentials, extra...)
	}
	handler, err := memory.NewHandlerWithCredentials(engine, credentials)
	if err != nil {
		engine.Close()
		return nil, err
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		engine.Close()
		return nil, err
	}
	if tcp, ok := listener.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		listener.Close()
		engine.Close()
		return nil, fmt.Errorf("memory resolved outside loopback")
	}
	ctx, cancel := context.WithCancel(parent)
	service := &memoryService{engine: engine, listener: listener, server: &http.Server{Handler: handler}, cancel: cancel, workerDone: make(chan struct{})}
	go func() {
		defer close(service.workerDone)
		if ready != nil {
			select {
			case <-ready:
			case <-ctx.Done():
				return
			}
		}
		engine.Run(ctx)
	}()
	go func() {
		if err := service.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("memory HTTP listener stopped: %v", err)
		}
	}()
	return service, nil
}
