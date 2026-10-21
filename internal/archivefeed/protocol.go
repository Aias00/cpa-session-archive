// Package archivefeed transports committed archive evidence over authenticated loopback HTTP.
package archivefeed

import (
	"errors"
	"strings"
	"unicode/utf8"

	"cpa-session-archive/internal/archive"
)

const (
	Prefix           = "/internal/archive-feed/v1/"
	HeaderVersion    = "X-CPA-Archive-Feed-Version"
	HeaderArchiveID  = "X-CPA-Archive-ID"
	Version          = "1"
	MaxSourceBytes   = 256 << 20
	MaxMetadataBytes = 1 << 20
)

var errUnavailable = errors.New("archive feed unavailable or invalid")

type manifest struct {
	Version         int    `json:"version"`
	ArchiveID       string `json:"archive_id"`
	Watermark       int64  `json:"watermark"`
	HistoryComplete bool   `json:"history_complete"`
}
type change struct {
	Sequence  int64  `json:"sequence"`
	RequestID string `json:"request_id"`
}
type changes struct {
	Changes []change `json:"changes"`
}
type page struct {
	IDs     []string `json:"ids"`
	AfterID int64    `json:"after_id"`
	Done    bool     `json:"done"`
}
type source struct {
	Record   archive.Record `json:"record"`
	Revision string         `json:"revision"`
	Sequence int64          `json:"sequence"`
}
type revision struct {
	Revision string `json:"revision"`
}
type failure struct {
	Error string `json:"error"`
}

func validIdentity(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
func validToken(token string) bool {
	if len(token) < 32 || len(token) > 4096 {
		return false
	}
	for _, c := range token {
		if c <= 32 || c >= 127 {
			return false
		}
	}
	return true
}
func validID(id string) bool {
	return id != "" && len(id) <= 4096 && utf8.ValidString(id) && !strings.ContainsRune(id, 0)
}
func validRevision(value string) bool {
	return value != "" && len(value) <= 4096 && utf8.ValidString(value)
}
func validChanges(values []change, after int64, limit int) bool {
	if values == nil || len(values) > limit {
		return false
	}
	for _, item := range values {
		if item.Sequence <= after || !validID(item.RequestID) {
			return false
		}
		after = item.Sequence
	}
	return true
}
func validPage(value page, after int64, limit int) bool {
	if value.IDs == nil || len(value.IDs) > limit || value.AfterID < after || len(value.IDs) > 0 && value.AfterID <= after || !value.Done && value.AfterID <= after {
		return false
	}
	if len(value.IDs) == 0 && (!value.Done || value.AfterID != after) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range value.IDs {
		if !validID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
