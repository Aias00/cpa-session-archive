package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func semanticEnvironment(t *testing.T) map[string]string {
	t.Helper()
	env := testEnvironment(t)
	env["MEMORY_EMBEDDING_ENDPOINT"] = "http://127.0.0.1:1"
	env["MEMORY_EMBEDDING_TOKEN_FILE"] = filepath.Join(t.TempDir(), "embedding.token")
	env["MEMORY_EMBEDDING_FINGERPRINT"] = "synthetic-model-v1-input-policy-r384-o96"
	env["MEMORY_EMBEDDING_DIMENSIONS"] = "2"
	if err := os.WriteFile(env["MEMORY_EMBEDDING_TOKEN_FILE"], []byte(strings.Repeat("e", 48)), 0600); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestEmbeddingConfigurationRequiresExplicitCompletePins(t *testing.T) {
	for _, scenario := range []string{"no-endpoint", "no-token", "no-fingerprint", "no-dimensions", "huge-dimensions", "nan", "negative", "infinite"} {
		t.Run(scenario, func(t *testing.T) {
			env := semanticEnvironment(t)
			switch scenario {
			case "no-endpoint":
				delete(env, "MEMORY_EMBEDDING_ENDPOINT")
			case "no-token":
				delete(env, "MEMORY_EMBEDDING_TOKEN_FILE")
			case "no-fingerprint":
				delete(env, "MEMORY_EMBEDDING_FINGERPRINT")
			case "no-dimensions":
				delete(env, "MEMORY_EMBEDDING_DIMENSIONS")
			case "huge-dimensions":
				env["MEMORY_EMBEDDING_DIMENSIONS"] = "1000000000"
			case "nan":
				env["MEMORY_SEMANTIC_MINIMUM"] = "NaN"
			case "negative":
				env["MEMORY_SEMANTIC_MINIMUM"] = "-1"
			case "infinite":
				env["MEMORY_SEMANTIC_MINIMUM"] = "+Inf"
			}
			if _, err := loadConfig(getter(env)); err == nil {
				t.Fatal("incomplete or invalid embedding configuration accepted")
			}
		})
	}
	cfg, err := loadConfig(getter(semanticEnvironment(t)))
	if err != nil || cfg.semanticMinimum != .8 || cfg.embeddingDimensions != 2 {
		t.Fatalf("explicit model configuration failed: %v", err)
	}
}

func TestEmbeddingNeverUsesFeedOrRecallCredentials(t *testing.T) {
	for _, scenario := range []string{"feed", "owner", "scoped"} {
		t.Run(scenario, func(t *testing.T) {
			env := semanticEnvironment(t)
			switch scenario {
			case "feed":
				env["MEMORY_EMBEDDING_TOKEN_FILE"] = env["MEMORY_ARCHIVE_TOKEN_FILE"]
			case "owner":
				env["MEMORY_EMBEDDING_TOKEN_FILE"] = env["MEMORY_READ_TOKEN_FILE"]
			case "scoped":
				env["MEMORY_READ_CREDENTIALS_FILE"] = filepath.Join(t.TempDir(), "credentials.json")
				if err := os.WriteFile(env["MEMORY_READ_CREDENTIALS_FILE"], []byte(`[{"token":"`+strings.Repeat("e", 48)+`","scope":{"project_id":"p"}}]`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := run(context.Background(), getter(env)); err == nil || !strings.Contains(err.Error(), "distinct") {
				t.Fatalf("shared embedding credential accepted: %v", err)
			}
		})
	}
}

func TestRemoteEmbeddingRejectedBeforeOpeningProjection(t *testing.T) {
	env := semanticEnvironment(t)
	env["MEMORY_EMBEDDING_ENDPOINT"] = "https://example.com"
	if err := run(context.Background(), getter(env)); err == nil {
		t.Fatal("remote model endpoint accepted")
	}
	if _, err := os.Stat(env["MEMORY_DB"]); !os.IsNotExist(err) {
		t.Fatal("invalid model setup modified projection")
	}
}
