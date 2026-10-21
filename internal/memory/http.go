package memory

import (
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

//go:embed web/index.html
var webHTML []byte

// NewHandler exposes owner-wide, read-only recall. The caller must bind it to
// loopback; this token is distinct from the proxy administration credential.
func NewHandler(reader Reader, token string) (http.Handler, error) {
	return NewHandlerWithCredentials(reader, []Credential{{Token: token, Scope: Scope{AllowAll: true}}})
}

// NewHandlerWithCredentials authorizes every data read on the server side.
func NewHandlerWithCredentials(reader Reader, credentials []Credential) (http.Handler, error) {
	type access struct {
		digest [32]byte
		reader Reader
	}
	permissions := []access{}
	seen := map[[32]byte]bool{}
	if len(credentials) == 0 {
		return nil, fmt.Errorf("at least one read credential is required")
	}
	for _, credential := range credentials {
		token := credential.Token
		if len(token) < 32 || len(token) > 4096 {
			return nil, fmt.Errorf("read token must contain 32 to 4096 printable ASCII characters")
		}
		for _, r := range token {
			if r <= 32 || r > 126 {
				return nil, fmt.Errorf("invalid read token format")
			}
		}
		digest := sha256.Sum256([]byte(token))
		if seen[digest] {
			return nil, fmt.Errorf("duplicate read credential")
		}
		seen[digest] = true
		scoped, err := WithScope(reader, credential.Scope)
		if err != nil {
			return nil, err
		}
		permissions = append(permissions, access{digest: digest, reader: scoped})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		fail := func(status int, text string) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": text})
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			fail(405, "GET required")
			return
		}
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
			_, _ = w.Write(webHTML)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			parsed, err := url.Parse(origin)
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			if err != nil || parsed.Scheme != scheme || parsed.Host != r.Host || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
				fail(403, "cross-origin access denied")
				return
			}
		}
		supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		got := sha256.Sum256([]byte(supplied))
		var reader Reader
		for _, permission := range permissions {
			if subtle.ConstantTimeCompare(got[:], permission.digest[:]) == 1 {
				reader = permission.reader
			}
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || reader == nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(401, "read credential required")
			return
		}
		values := r.URL.Query()
		integer := func(key string, optional bool) (int, error) {
			raw := values.Get(key)
			if raw == "" && optional {
				return 0, nil
			}
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 {
				return 0, ErrInvalidQuery
			}
			return value, nil
		}
		var result any
		var err error
		switch r.URL.Path {
		case "/v1/status":
			result, err = reader.Status(r.Context())
		case "/v1/search", "/v1/recap":
			limit, parseErr := integer("limit", true)
			if parseErr != nil {
				fail(400, "invalid query")
				return
			}
			q := Query{Text: values.Get("q"), ProjectID: values.Get("project_id"), Client: values.Get("client"), SessionID: values.Get("session_id"), After: values.Get("after"), Before: values.Get("before"), Mode: values.Get("mode"), Limit: limit}
			if r.URL.Path == "/v1/search" {
				result, err = reader.Search(r.Context(), q)
			} else {
				result, err = reader.Recap(r.Context(), q)
			}
		case "/v1/source":
			start, e1 := integer("start", false)
			end, e2 := integer("end", false)
			version, e3 := integer("normalizer_version", false)
			if e1 != nil || e2 != nil || e3 != nil {
				fail(400, "invalid citation")
				return
			}
			result, err = reader.ReadSource(r.Context(), Citation{RequestID: values.Get("request_id"), Revision: values.Get("revision"), Side: values.Get("side"), Pointer: values.Get("pointer"), Start: start, End: end, NormalizerVersion: version})
		default:
			fail(404, "not found")
			return
		}
		if err != nil {
			switch {
			case errors.Is(err, ErrInvalidQuery):
				fail(400, "invalid query or missing project/session scope")
			case errors.Is(err, ErrSourceUnavailable):
				fail(410, "source revision unavailable")
			case errors.Is(err, ErrQueryBudget):
				fail(422, ErrQueryBudget.Error())
			case errors.Is(err, ErrSemanticUnavailable):
				fail(503, ErrSemanticUnavailable.Error())
			default:
				fail(500, "recall unavailable")
			}
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(result)
	}), nil
}
