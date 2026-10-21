package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrUnavailable  = errors.New("local embedding service unavailable or invalid")
	ErrInvalidInput = errors.New("invalid embedding purpose, batch or text")
)

const maxResponseBytes = 1 << 20

type httpResponse struct {
	Fingerprint *string
	Dimensions  *int
	Vectors     *[][]strictComponent
}

type strictComponent float32

func (c *strictComponent) UnmarshalJSON(raw []byte) error {
	// Plain float32 decoding silently turns JSON null into zero.
	value, err := strconv.ParseFloat(string(raw), 32)
	if err != nil {
		return ErrUnavailable
	}
	*c = strictComponent(value)
	return nil
}

// Read control fields exactly once, with exact names. encoding/json's normal
// struct decoding otherwise accepts duplicate keys and case-insensitive aliases.
func decodeResponse(raw []byte) (httpResponse, error) {
	var value httpResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return value, ErrUnavailable
	}
	seen := make(map[string]bool, 3)
	for decoder.More() {
		token, err := decoder.Token()
		field, ok := token.(string)
		if err != nil || !ok || seen[field] {
			return value, ErrUnavailable
		}
		seen[field] = true
		var destination any
		switch field {
		case "fingerprint":
			destination = &value.Fingerprint
		case "dimensions":
			destination = &value.Dimensions
		case "vectors":
			destination = &value.Vectors
		default:
			return value, ErrUnavailable
		}
		if decoder.Decode(destination) != nil {
			return value, ErrUnavailable
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || decoder.Decode(new(any)) != io.EOF {
		return value, ErrUnavailable
	}
	return value, nil
}

type HTTPClient struct {
	endpoint    string
	token       string
	fingerprint string
	dimensions  int
	http        *http.Client
}

var _ Embedder = (*HTTPClient)(nil)

// NewHTTP accepts only a literal loopback origin. Tokens never enter URLs,
// system proxies, redirects, returned backend errors or request diagnostics.
func NewHTTP(endpoint, token, fingerprint string, dimensions int) (*HTTPClient, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u == nil || u.Scheme != "http" || u.Opaque != "" || u.User != nil || u.Host == "" || strings.HasSuffix(u.Host, ":") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		return nil, errors.New("embedding endpoint must be a literal loopback HTTP origin")
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("invalid embedding endpoint port")
		}
	}
	if !printable(token, 32, 4096) || !printable(fingerprint, 1, 1024) || dimensions < 1 || dimensions > 4096 {
		return nil, errors.New("invalid embedding credential or model identity")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext, DisableCompression: true, MaxIdleConns: 2, MaxConnsPerHost: 2, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	return &HTTPClient{endpoint: "http://" + u.Host + "/embed", token: token, fingerprint: fingerprint, dimensions: dimensions, http: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func printable(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, r := range value {
		if r <= 32 || r >= 127 {
			return false
		}
	}
	return true
}

func (c *HTTPClient) Fingerprint() string { return c.fingerprint }
func (c *HTTPClient) Dimensions() int     { return c.dimensions }
func (c *HTTPClient) Close() error        { c.http.CloseIdleConnections(); return nil }

func (c *HTTPClient) Embed(ctx context.Context, purpose Purpose, texts []string) ([][]float32, error) {
	maxRunes := 384
	if purpose == Query {
		maxRunes = 256
	} else if purpose != Document {
		return nil, ErrInvalidInput
	}
	if len(texts) == 0 || len(texts) > MaxBatch {
		return nil, ErrInvalidInput
	}
	for _, value := range texts {
		if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > maxRunes {
			return nil, ErrInvalidInput
		}
	}
	body, err := json.Marshal(struct {
		Fingerprint string   `json:"fingerprint"`
		Purpose     Purpose  `json:"purpose"`
		Texts       []string `json:"texts"`
	}{c.fingerprint, purpose, texts})
	if err != nil || len(body) > 64<<10 {
		return nil, ErrInvalidInput
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, ErrUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || err != nil || media != "application/json" || len(response.Header.Values("Content-Type")) != 1 || len(response.Header.Values("Content-Encoding")) != 0 || response.ContentLength > maxResponseBytes {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	value, err := decodeResponse(raw)
	if err != nil || value.Fingerprint == nil || *value.Fingerprint != c.fingerprint || value.Dimensions == nil || *value.Dimensions != c.dimensions || value.Vectors == nil || len(*value.Vectors) != len(texts) {
		return nil, ErrUnavailable
	}
	vectors := make([][]float32, len(*value.Vectors))
	for index, vector := range *value.Vectors {
		if len(vector) != c.dimensions {
			return nil, ErrUnavailable
		}
		vectors[index] = make([]float32, len(vector))
		norm := float64(0)
		for column, component := range vector {
			if math.IsNaN(float64(component)) || math.IsInf(float64(component), 0) {
				return nil, ErrUnavailable
			}
			vectors[index][column] = float32(component)
			norm += float64(component) * float64(component)
		}
		if norm == 0 {
			return nil, ErrUnavailable
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return vectors, nil
}
