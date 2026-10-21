package recallruntime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"cpa-session-archive/internal/memory"
)

func readPrivateFile(path, label string, limit int64) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("%s file is required", label)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%s file must be a private regular file (0600)", label)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open %s file", label)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%s file must be a private regular file (0600)", label)
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, fmt.Errorf("invalid or oversized %s file", label)
	}
	return raw, nil
}

// ReadTokenFile reads a bounded private credential without logging its contents.
func ReadTokenFile(path string) (string, error) {
	raw, err := readPrivateFile(path, "token", 4096)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if len(token) < 32 || len(token) > 4096 {
		return "", fmt.Errorf("invalid token format")
	}
	for _, r := range token {
		if r <= 32 || r > 126 {
			return "", fmt.Errorf("invalid token format")
		}
	}
	return token, nil
}

func loadCredentials(cfg Config) ([]memory.Credential, error) {
	token, err := ReadTokenFile(cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	credentials := []memory.Credential{{Token: token, Scope: memory.Scope{AllowAll: true}}}
	if cfg.CredentialsFile == "" {
		if token == cfg.ReservedToken {
			return nil, fmt.Errorf("archive feed and recall credentials must be distinct")
		}
		return credentials, nil
	}
	raw, err := readPrivateFile(cfg.CredentialsFile, "additional credentials", 65536)
	if err != nil {
		return nil, err
	}
	var extra []memory.Credential
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&extra); err != nil {
		return nil, fmt.Errorf("invalid additional credentials JSON")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("invalid additional credentials JSON")
	}
	credentials = append(credentials, extra...)
	for _, credential := range credentials {
		if credential.Token == cfg.ReservedToken {
			return nil, fmt.Errorf("archive feed and recall credentials must be distinct")
		}
	}
	return credentials, nil
}
