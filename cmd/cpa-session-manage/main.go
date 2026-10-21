// cpa-session-manage organizes sessions through the authenticated CPA API.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cpa-session-archive/internal/archive"
)

const managementPrefix = "/v0/management/plugins/cpa-session-archive"

type repeatedFlag []string

func (v *repeatedFlag) String() string { return strings.Join(*v, ",") }
func (v *repeatedFlag) Set(value string) error {
	*v = append(*v, value)
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "session manager:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: cpa-session-manage list|show|edit --endpoint CPA_ORIGIN --token-file PRIVATE_FILE [options]")
	}
	action := args[0]
	if action != "list" && action != "show" && action != "edit" {
		return errors.New("action must be list, show or edit")
	}
	flags := flag.NewFlagSet("cpa-session-manage "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("endpoint", "", "CPA management origin; HTTPS or literal-loopback HTTP")
	tokenFile := flags.String("token-file", "", "private file containing the CPA management credential")
	var sessionID, title, note, starred, archived string
	var expected int64
	var clearTags bool
	var tags, facets repeatedFlag
	query := url.Values{}
	var search, tag string
	var limit, offset int
	if action == "list" {
		flags.StringVar(&search, "q", "", "literal search of session labels, summary, notes and tags")
		flags.StringVar(&tag, "tag", "", "exact user tag")
		flags.IntVar(&limit, "limit", 100, "page size, 1..1000")
		flags.IntVar(&offset, "offset", 0, "live list offset, 0..1000000")
		flags.StringVar(&archived, "archived", "exclude", "exclude, only or all")
		flags.StringVar(&starred, "starred", "false", "true selects favorites only")
		flags.Var(&facets, "filter", "exact facet NAME=VALUE; repeat for AND")
	} else {
		flags.StringVar(&sessionID, "session-id", "", "exact archived session ID")
		if action == "edit" {
			flags.Int64Var(&expected, "expected-revision", -1, "revision inspected with show; required")
			flags.StringVar(&title, "title", "", "custom title; empty resets to source summary")
			flags.StringVar(&note, "note", "", "user note; empty clears it")
			flags.Var(&tags, "tag", "replacement user tag; repeat for several tags")
			flags.BoolVar(&clearTags, "clear-tags", false, "clear all tags")
			flags.StringVar(&starred, "starred", "", "true or false")
			flags.StringVar(&archived, "archived", "", "true archives; false restores")
		}
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	changed := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { changed[f.Name] = true })
	if action == "list" {
		if limit < 1 || limit > 1000 || offset < 0 || offset > 1000000 || (archived != "exclude" && archived != "only" && archived != "all") || (starred != "true" && starred != "false") {
			return errors.New("invalid list limit, offset, archived or starred option")
		}
		query.Set("limit", strconv.Itoa(limit))
		query.Set("offset", strconv.Itoa(offset))
		query.Set("q", search)
		query.Set("tag", tag)
		query.Set("archived", archived)
		query.Set("starred", starred)
		for _, filter := range facets {
			name, value, ok := strings.Cut(filter, "=")
			if !ok || name == "" || value == "" || query.Has(name) {
				return errors.New("filter must be a unique facet NAME=VALUE without reserved query names")
			}
			query.Set(name, value)
		}
	} else {
		if sessionID == "" {
			return errors.New("session-id is required")
		}
		query.Set("session_id", sessionID)
		if action == "edit" {
			if expected < 0 {
				return errors.New("expected-revision from show is required")
			}
			if !changed["title"] && !changed["note"] && !changed["tag"] && !clearTags && !changed["starred"] && !changed["archived"] {
				return errors.New("specify at least one metadata change")
			}
			if clearTags && len(tags) > 0 {
				return errors.New("tag and clear-tags cannot be combined")
			}
			for _, name := range []string{"starred", "archived"} {
				value := starred
				if name == "archived" {
					value = archived
				}
				if changed[name] && value != "true" && value != "false" {
					return fmt.Errorf("%s must be true or false", name)
				}
			}
		}
	}
	client, err := newClient(*endpoint, *tokenFile)
	if err != nil {
		return err
	}
	defer client.transport.CloseIdleConnections()
	if action == "list" {
		var page archive.ManagedSessionPage
		if err = client.request(ctx, http.MethodGet, "/session-browser", query, nil, &page); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(page)
	}
	var metadata archive.SessionManagement
	if err = client.request(ctx, http.MethodGet, "/session-metadata", query, nil, &metadata); err != nil {
		return err
	}
	if action == "show" {
		return json.NewEncoder(stdout).Encode(metadata)
	}
	if metadata.Revision != expected {
		return errors.New("metadata changed; run show and review the current revision before editing")
	}
	update := struct {
		ExpectedRevision int64 `json:"expected_revision"`
		archive.SessionManagementUpdate
	}{ExpectedRevision: expected}
	update.Title, update.Note, update.Tags = metadata.Title, metadata.Note, metadata.Tags
	update.Starred, update.Archived = metadata.Starred, metadata.Archived
	if changed["title"] {
		update.Title = title
	}
	if changed["note"] {
		update.Note = note
	}
	if len(tags) > 0 || clearTags {
		update.Tags = []string(tags)
	}
	if changed["starred"] {
		update.Starred = starred == "true"
	}
	if changed["archived"] {
		update.Archived = archived == "true"
	}
	if err = client.request(ctx, http.MethodPut, "/session-metadata", query, update, &metadata); err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(metadata)
}

type managementClient struct {
	origin, token string
	http          *http.Client
	transport     *http.Transport
}

func newClient(endpoint, tokenFile string) (*managementClient, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("endpoint must be a CPA management origin without credentials, path, query or fragment")
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return nil, errors.New("HTTP endpoint requires a literal loopback address")
		}
	}
	file, err := os.Open(tokenFile)
	if err != nil {
		return nil, errors.New("cannot open management credential file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("management credential must be in a private regular file (0600)")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	token := strings.TrimSpace(string(raw))
	if err != nil || len(raw) > 4096 || token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("invalid management credential file")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &managementClient{origin: strings.TrimSuffix(endpoint, "/"), token: token, http: client, transport: transport}, nil
}

func (c *managementClient) request(ctx context.Context, method, path string, query url.Values, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return errors.New("cannot encode metadata update")
		}
		body = bytes.NewReader(raw)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.origin+managementPrefix+path+"?"+query.Encode(), body)
	if err != nil {
		return errors.New("cannot construct management request")
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	if input != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(r)
	if err != nil {
		return errors.New("cannot reach CPA management API")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		return errors.New("metadata changed; run show and review the current revision before editing")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("CPA management API returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<20)+1))
	if err != nil || len(raw) > 64<<20 || json.Unmarshal(raw, output) != nil {
		return errors.New("invalid management API response")
	}
	return nil
}
