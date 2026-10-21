package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"

	"cpa-session-archive/internal/archivefeed"
	"cpa-session-archive/internal/ingeststatus"
	"cpa-session-archive/internal/storage"
)

// The collector exports committed evidence, not a memory implementation. A
// failed optional feed setup never prevents the ingestion listener from starting.
func configureArchiveFeed(ctx context.Context, backend *storage.SQLite, listen, tokenFile string, ready <-chan struct{}, diagnostics func() ingeststatus.Status) (http.Handler, error) {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || (host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback())) {
		return nil, errors.New("archive feed requires a loopback collector listener")
	}
	info, err := os.Lstat(tokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("archive feed credential must be a private regular file")
	}
	file, err := os.OpenFile(tokenFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("archive feed credential file unavailable")
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("archive feed credential must be a private regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(raw) > 4096 {
		return nil, errors.New("archive feed credential invalid")
	}
	id, err := backend.EnsureFeedIdentity(ctx)
	if err != nil {
		return nil, errors.New("archive feed identity unavailable")
	}
	return archivefeed.NewHandler(backend, archivefeed.ServerConfig{ArchiveID: id, Token: strings.TrimSpace(string(raw)), Ready: ready, Diagnostics: diagnostics, HistoryComplete: true})
}
