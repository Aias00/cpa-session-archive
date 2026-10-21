package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHybridUnavailableIsExplicitAndDoesNotChangeLiteral(t *testing.T) {
	e, _, _ := testEngine(t)
	if _, err := e.Search(context.Background(), Query{Text: "evidence", Mode: "hybrid"}); !errors.Is(err, ErrSemanticUnavailable) {
		t.Fatalf("unconfigured hybrid was not unavailable: %v", err)
	}
	if _, err := e.Search(context.Background(), Query{Text: "evidence"}); err != nil {
		t.Fatalf("optional provider changed literal retrieval: %v", err)
	}
	token := strings.Repeat("r", 48)
	handler, err := NewHandler(e, token)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/search?q=evidence&mode=hybrid", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var failure map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusServiceUnavailable || len(failure) != 1 || failure["error"] != ErrSemanticUnavailable.Error() {
		t.Fatal("unavailable semantic path became an empty success")
	}
}
