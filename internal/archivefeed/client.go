package archivefeed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/ingeststatus"
	"cpa-session-archive/internal/storage"
)

type Client struct {
	base      string
	token     string
	http      *http.Client
	transport *http.Transport
	lifetime  context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	identity  string
}

var _ storage.MemoryFeed = (*Client)(nil)
var _ storage.IdentityFeed = (*Client)(nil)
var _ storage.RevisionFeed = (*Client)(nil)

func NewClient(base, token string) (*Client, error) {
	origin, err := url.Parse(base)
	if err != nil || origin == nil || origin.Scheme != "http" || origin.Opaque != "" || origin.User != nil || origin.Host == "" || strings.HasSuffix(origin.Host, ":") || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" || origin.RawFragment != "" || origin.RawPath != "" || origin.Path != "" && origin.Path != "/" || net.ParseIP(origin.Hostname()) == nil || !net.ParseIP(origin.Hostname()).IsLoopback() || !validToken(token) {
		return nil, errors.New("archive feed requires a literal loopback HTTP origin and token")
	}
	if origin.Port() != "" {
		port, err := strconv.Atoi(origin.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("invalid archive feed origin")
		}
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, DisableCompression: true, MaxIdleConns: 8, MaxConnsPerHost: 8, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	lifetime, cancel := context.WithCancel(context.Background())
	return &Client{base: "http://" + origin.Host, token: token, transport: transport, http: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, lifetime: lifetime, cancel: cancel}, nil
}
func (c *Client) Close() error {
	c.cancel()
	c.transport.CloseIdleConnections()
	return nil
}

func (c *Client) checkIdentity(value string, pin bool) error {
	if !validIdentity(value) {
		return errUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.identity != "" && c.identity != value {
		return errors.New("archive feed identity changed")
	}
	if pin {
		c.identity = value
	}
	return nil
}

// Protocol control fields are required and unique. In particular an ambiguous
// error or changes object must never become a tombstone or an empty event page.
// Decode each field directly so large source records need no second raw copy.
type requiredField struct{ target any }

func (field *requiredField) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errUnavailable
	}
	return json.Unmarshal(data, field.target)
}

func decode(data []byte, out any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errUnavailable
	}
	var fields map[string]any
	switch value := out.(type) {
	case *manifest:
		fields = map[string]any{"version": &value.Version, "archive_id": &value.ArchiveID, "watermark": &value.Watermark, "history_complete": &value.HistoryComplete}
	case *changes:
		fields = map[string]any{"changes": &value.Changes}
	case *page:
		fields = map[string]any{"ids": &value.IDs, "after_id": &value.AfterID, "done": &value.Done}
	case *source:
		fields = map[string]any{"record": &value.Record, "revision": &value.Revision, "sequence": &value.Sequence}
	case *revision:
		fields = map[string]any{"revision": &value.Revision}
	case *failure:
		fields = map[string]any{"error": &value.Error}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if fields == nil {
		if decoder.Decode(out) != nil {
			return errUnavailable
		}
	} else {
		start, err := decoder.Token()
		if err != nil || start != json.Delim('{') {
			return errUnavailable
		}
		seen := map[string]bool{}
		for decoder.More() {
			token, err := decoder.Token()
			key, valid := token.(string)
			field, exists := fields[key]
			if err != nil || !valid || !exists || seen[key] {
				return errUnavailable
			}
			seen[key] = true
			if decoder.Decode(&requiredField{field}) != nil {
				return errUnavailable
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') || len(seen) != len(fields) {
			return errUnavailable
		}
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errUnavailable
	}
	return nil
}

func (c *Client) get(ctx context.Context, name string, query url.Values, out any, maxBytes int64, missing bool) (string, error) {
	requestContext, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.lifetime, cancel)
	defer stop()
	defer cancel()
	if c.lifetime.Err() != nil {
		return "", errUnavailable
	}
	endpoint := c.base + Prefix + name
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", errUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errUnavailable
	}
	defer response.Body.Close()
	id := response.Header.Get(HeaderArchiveID)
	if len(response.Header.Values(HeaderVersion)) != 1 || len(response.Header.Values(HeaderArchiveID)) != 1 || response.Header.Get(HeaderVersion) != Version || c.checkIdentity(id, false) != nil {
		return "", errUnavailable
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || response.Header.Get("Content-Encoding") != "" {
		return "", errUnavailable
	}
	if response.StatusCode != 200 {
		maxBytes = MaxMetadataBytes
	}
	if response.ContentLength > maxBytes {
		return "", errUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errUnavailable
	}
	if response.StatusCode == 404 && missing {
		var failure failure
		if decode(data, &failure) != nil || failure.Error != "source_missing" || c.checkIdentity(id, true) != nil {
			return "", errUnavailable
		}
		return id, storage.ErrSourceNotFound
	}
	if response.StatusCode != 200 || decode(data, out) != nil {
		return "", errUnavailable
	}
	return id, nil
}
func (c *Client) MemoryIdentity(ctx context.Context) (storage.FeedIdentity, error) {
	var value manifest
	id, err := c.get(ctx, "manifest", nil, &value, MaxMetadataBytes, false)
	if err != nil || value.Version != 1 || value.ArchiveID != id || value.Watermark < 0 {
		if err != nil {
			return storage.FeedIdentity{}, err
		}
		return storage.FeedIdentity{}, errUnavailable
	}
	if err := c.checkIdentity(id, true); err != nil {
		return storage.FeedIdentity{}, err
	}
	return storage.FeedIdentity{ArchiveID: id, Watermark: value.Watermark, HistoryComplete: value.HistoryComplete}, nil
}
func (c *Client) MemoryWatermark(ctx context.Context) (int64, error) {
	value, err := c.MemoryIdentity(ctx)
	return value.Watermark, err
}
func cursor(after int64, limit int, key string) (url.Values, error) {
	if after < 0 || limit < 1 || limit > 256 {
		return nil, errors.New("invalid archive feed cursor or page size")
	}
	return url.Values{key: {strconv.FormatInt(after, 10)}, "limit": {strconv.Itoa(limit)}}, nil
}
func (c *Client) MemoryChanges(ctx context.Context, after int64, limit int) ([]archive.SourceChange, error) {
	query, err := cursor(after, limit, "after")
	if err != nil {
		return nil, err
	}
	var value changes
	id, err := c.get(ctx, "changes", query, &value, MaxMetadataBytes, false)
	if err != nil {
		return nil, err
	}
	if !validChanges(value.Changes, after, limit) || c.checkIdentity(id, true) != nil {
		return nil, errUnavailable
	}
	result := make([]archive.SourceChange, 0, len(value.Changes))
	for _, item := range value.Changes {
		result = append(result, archive.SourceChange{Sequence: item.Sequence, RequestID: item.RequestID})
	}
	return result, nil
}
func (c *Client) MemoryBackfill(ctx context.Context, after int64, limit int) (archive.SourcePage, error) {
	query, err := cursor(after, limit, "after_id")
	if err != nil {
		return archive.SourcePage{}, err
	}
	var value page
	id, err := c.get(ctx, "backfill", query, &value, MaxMetadataBytes, false)
	if err != nil {
		return archive.SourcePage{}, err
	}
	if !validPage(value, after, limit) || c.checkIdentity(id, true) != nil {
		return archive.SourcePage{}, errUnavailable
	}
	return archive.SourcePage{IDs: value.IDs, AfterID: value.AfterID, Done: value.Done}, nil
}
func (c *Client) MemorySource(ctx context.Context, requestID string) (archive.SourceSnapshot, error) {
	if !validID(requestID) {
		return archive.SourceSnapshot{}, errUnavailable
	}
	var value source
	id, err := c.get(ctx, "source", url.Values{"id": {requestID}}, &value, MaxSourceBytes, true)
	if err != nil {
		return archive.SourceSnapshot{}, err
	}
	if value.Record.RequestID != requestID || !validRevision(value.Revision) || value.Sequence < 0 || c.checkIdentity(id, true) != nil {
		return archive.SourceSnapshot{}, errUnavailable
	}
	return archive.SourceSnapshot{Record: value.Record, Revision: value.Revision, Sequence: value.Sequence}, nil
}
func (c *Client) MemoryRevision(ctx context.Context, requestID string) (string, error) {
	if !validID(requestID) {
		return "", errUnavailable
	}
	var value revision
	id, err := c.get(ctx, "revision", url.Values{"id": {requestID}}, &value, MaxMetadataBytes, true)
	if err != nil {
		return "", err
	}
	if !validRevision(value.Revision) || c.checkIdentity(id, true) != nil {
		return "", errUnavailable
	}
	return value.Revision, nil
}
func (c *Client) IngestionStatus(ctx context.Context) (ingeststatus.Status, error) {
	var value ingeststatus.Status
	id, err := c.get(ctx, "ingestion", nil, &value, MaxMetadataBytes, false)
	if err != nil {
		return value, err
	}
	if err := c.checkIdentity(id, true); err != nil {
		return ingeststatus.Status{}, err
	}
	return value, nil
}
