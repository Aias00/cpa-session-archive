// Package recall bridges the collector's authenticated API to local clients.
package recall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"cpa-session-archive/internal/memory"
)

type Client struct {
	base  string
	token string
	http  *http.Client
}

var _ memory.Reader = (*Client)(nil)

func NewClient(base, token string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("invalid recall endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("recall endpoint must be a loopback HTTP origin without credentials or query")
	}
	if len(token) < 32 || len(token) > 4096 {
		return nil, fmt.Errorf("invalid recall read credential")
	}
	for _, r := range token {
		if r <= 32 || r > 126 {
			return nil, fmt.Errorf("invalid recall read credential")
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) get(ctx context.Context, path string, query url.Values, result any) error {
	endpoint := c.base + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("invalid recall request")
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return errors.New("recall service unavailable; check the local SSH tunnel")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 200:
	case 400:
		return memory.ErrInvalidQuery
	case 410:
		return memory.ErrSourceUnavailable
	case 422:
		return memory.ErrQueryBudget
	case 401, 403:
		return errors.New("recall read credential rejected or outside its scope")
	default:
		return errors.New("recall service request failed")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return errors.New("recall response exceeded the size limit or was interrupted")
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return errors.New("invalid recall response")
	}
	return nil
}
func queryValues(q memory.Query) url.Values {
	values := url.Values{}
	for _, pair := range []struct{ k, v string }{{"q", q.Text}, {"project_id", q.ProjectID}, {"session_id", q.SessionID}, {"client", q.Client}, {"after", q.After}, {"before", q.Before}, {"mode", q.Mode}} {
		if pair.v != "" {
			values.Set(pair.k, pair.v)
		}
	}
	if q.Limit != 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	return values
}
func (c *Client) Search(ctx context.Context, q memory.Query) (memory.SearchResult, error) {
	var out memory.SearchResult
	err := c.get(ctx, "/v1/search", queryValues(q), &out)
	return out, err
}
func (c *Client) Recap(ctx context.Context, q memory.Query) (memory.RecapResult, error) {
	var out memory.RecapResult
	err := c.get(ctx, "/v1/recap", queryValues(q), &out)
	return out, err
}
func (c *Client) Status(ctx context.Context) (memory.Status, error) {
	var out memory.Status
	err := c.get(ctx, "/v1/status", nil, &out)
	return out, err
}
func (c *Client) ReadSource(ctx context.Context, citation memory.Citation) (memory.Evidence, error) {
	values := url.Values{"request_id": {citation.RequestID}, "revision": {citation.Revision}, "side": {citation.Side}, "pointer": {citation.Pointer}, "start": {strconv.Itoa(citation.Start)}, "end": {strconv.Itoa(citation.End)}, "normalizer_version": {strconv.Itoa(citation.NormalizerVersion)}}
	var out memory.Evidence
	err := c.get(ctx, "/v1/source", values, &out)
	return out, err
}
