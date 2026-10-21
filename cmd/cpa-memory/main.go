// cpa-memory serves read-only archive recall tools over standard MCP stdio.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"cpa-session-archive/internal/knowledge"
	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/projectcontext"
	"cpa-session-archive/internal/recall"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "recall bridge:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	reviewing := len(args) > 0 && args[0] == "review"
	if reviewing {
		args = args[1:]
	}
	flags := flag.NewFlagSet("cpa-memory", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	endpoint := flags.String("endpoint", "http://127.0.0.1:18319", "loopback recall API origin")
	tokenFile := flags.String("token-file", "", "private file containing the dedicated read token")
	project := flags.String("project-id", "", "restrict this bridge to an exact project")
	session := flags.String("session-id", "", "restrict this bridge to an exact session")
	owner := flags.Bool("allow-all-projects", false, "explicitly permit owner-wide recall with an owner-wide credential")
	knowledgeRoot := flags.String("knowledge-root", "", "existing canonical Git knowledge repository")
	projectsFile := flags.String("projects-file", "", "owner-configured JSON map of project IDs to Git roots")
	allowProposals := flags.Bool("allow-proposals", false, "expose sourced draft proposals; never expose review over MCP")
	reviewID := flags.String("id", "", "reviewed memory identifier")
	expected := flags.String("expected-revision", "", "exact reviewed draft/entry content revision")
	action := flags.String("action", "approve", "review action: approve or archive")
	global := flags.Bool("global", false, "explicitly review reusable global knowledge")
	note := flags.String("note", "", "human evidence/scope review attestation")
	confirmed := flags.Bool("confirm-reviewed", false, "confirm a human reviewed the content, evidence and scope")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	// Fail closed before opening a transport; a missing scope is never owner access.
	scope := memory.Scope{ProjectID: *project, SessionID: *session, AllowAll: *owner}
	if !*owner && *project == "" && *session == "" {
		return fmt.Errorf("set project-id, session-id or explicitly allow-all-projects")
	}
	if *tokenFile == "" {
		return fmt.Errorf("token-file is required")
	}
	file, err := os.Open(*tokenFile)
	if err != nil {
		return fmt.Errorf("cannot open recall token file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("recall token file must be a private regular file (0600)")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(raw) > 4096 {
		return fmt.Errorf("invalid recall token file")
	}
	reader, err := recall.NewClient(*endpoint, strings.TrimSpace(string(raw)))
	if err != nil {
		return err
	}
	defer reader.Close()
	scoped, err := memory.WithScope(reader, scope)
	if err != nil {
		return err
	}
	if *knowledgeRoot != "" || *projectsFile != "" {
		projects := map[string]string{}
		if *projectsFile != "" {
			file, err := os.Open(*projectsFile)
			if err != nil {
				return fmt.Errorf("cannot read project authority map")
			}
			raw, err := io.ReadAll(io.LimitReader(file, 65537))
			file.Close()
			if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &projects) != nil {
				return fmt.Errorf("invalid project authority map")
			}
		}
		library, err := knowledge.New(knowledge.Config{GlobalRepository: *knowledgeRoot, Projects: projects, Scope: scope, Source: scoped})
		if err != nil {
			return err
		}
		if reviewing {
			entry, err := library.Review(ctx, knowledge.Review{ID: *reviewID, ProjectID: *project, Global: *global, ExpectedRevision: *expected, Action: *action, Note: *note, Confirmed: *confirmed})
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(entry)
		}
		server := recall.NewServerWithKnowledge(scoped, library, *allowProposals)
		if len(projects) > 0 && scope.SessionID == "" {
			packets, err := projectcontext.New(projectcontext.Config{Projects: projects, Scope: scope, Source: scoped, Knowledge: library})
			if err != nil {
				return err
			}
			recall.AddProjectContextTools(server, packets)
		}
		return server.Run(ctx, &mcp.StdioTransport{})
	}
	if reviewing || *allowProposals {
		return fmt.Errorf("knowledge-root or projects-file is required")
	}
	return recall.NewServer(scoped).Run(ctx, &mcp.StdioTransport{})
}
