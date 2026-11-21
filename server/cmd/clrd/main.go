// Command clrd runs the clrd server and manages its tokens.
//
//	clrd [serve]
//	clrd token create --name pixel-9 --scope read,publish [--topics a,b]
//	clrd token list
//	clrd import ntfy --auth-file user.db --cache-file cache.db --attachment-dir DIR --default-access deny-all [--clear-older-than 72h] [--dry-run]
//	clrd health
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/thejdubb02/clrd/server"
	"github.com/thejdubb02/clrd/server/mcp"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "clrd:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	opt, err := server.ConfigFromEnv()
	if err != nil {
		return err
	}
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "help", "-h", "-help", "--help":
		fmt.Print(usage)
		return nil
	case "serve":
		s, err := server.New(opt)
		if err != nil {
			return err
		}
		defer s.Close()
		s.Mount("/mcp", mcp.New(s))
		mountWeb(s, opt.BaseURL, opt.HidePublicPages)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return s.Run(ctx)
	case "token":
		return tokenCmd(opt, args)
	case "health":
		return health(opt)
	case "import":
		return importCmd(opt, args)
	}
	return fmt.Errorf("unknown command %q (want serve, token, import or health; see clrd help)", cmd)
}

const usage = `clrd: self-hosted notifications. Dismiss once, gone everywhere.

  clrd [serve]                 run the server (no settings needed)
  clrd token create [--name me] [--scope read,publish|admin] [--topics a,b] [--approve allowed]
  clrd token list
  clrd import ntfy --auth-file user.db --cache-file cache.db --default-access deny-all [--dry-run]
  clrd health                  exit 0 if the local server answers /healthz

Settings are optional environment variables: CLRD_LISTEN (:8080), CLRD_DB
(see the first log line), CLRD_BASE_URL (your public https URL), and more in
docs/self-host.md. With no admin token yet, the server logs a one-time setup link.
`

func tokenCmd(opt server.Options, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("want token create or token list")
	}
	s, err := server.New(opt)
	if err != nil {
		return err
	}
	defer s.Close()
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("token create", flag.ContinueOnError)
		name := fs.String("name", "cli", "device name, shown as state_by")
		scope := fs.String("scope", "read,publish", "comma list of read, publish, admin")
		topics := fs.String("topics", "", "optional comma list of allowed topics")
		approve := fs.String("approve", "denied", "allowed lets it answer approval requests (Allow on an agent prompt)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		t, raw, err := s.CreateToken(context.Background(), *name, *approve, split(*scope), split(*topics))
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "created %s (%s), scopes %s, in %s. Shown once:\n", t.Name, t.ID, strings.Join(t.Scopes, ","), s.DBPath())
		fmt.Println(raw)
	case "list":
		ts, err := s.Tokens(context.Background(), time.Time{})
		if err != nil {
			return err
		}
		for _, t := range ts {
			seen := "never"
			if t.LastSeenAt != nil {
				seen = t.LastSeenAt.Format(time.RFC3339)
			}
			grants := ""
			for _, g := range t.Grants {
				grants += fmt.Sprintf(" %s=%s", g.Topic, strings.Join(g.Scopes, "+"))
			}
			fmt.Printf("%s\t%s\t%s\ttopics=%s\tgrants=%s\tseen=%s\n", t.ID, t.Name, strings.Join(t.Scopes, ","), strings.Join(t.Topics, ","), strings.TrimSpace(grants), seen)
		}
	default:
		return fmt.Errorf("unknown token command %q", args[0])
	}
	return nil
}

// importCmd copies another server's data in. Only ntfy for now (issue #117).
func importCmd(opt server.Options, args []string) error {
	if len(args) == 0 || args[0] != "ntfy" {
		return fmt.Errorf("want import ntfy")
	}
	fs := flag.NewFlagSet("import ntfy", flag.ContinueOnError)
	var in server.NtfyImport
	fs.StringVar(&in.AuthFile, "auth-file", "", "ntfy's auth-file (user.db)")
	fs.StringVar(&in.CacheFile, "cache-file", "", "ntfy's cache-file (cache.db)")
	fs.StringVar(&in.AttachmentDir, "attachment-dir", "", "ntfy's attachment-cache-dir")
	fs.StringVar(&in.DefaultAccess, "default-access", "", "ntfy's auth-default-access: read-write, read-only, write-only or deny-all")
	fs.DurationVar(&in.ClearOlderThan, "clear-older-than", 0, "messages older than this arrive dismissed (0 keeps all open)")
	fs.BoolVar(&in.DryRun, "dry-run", false, "print what maps and what does not, change nothing")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if in.AuthFile == "" && in.CacheFile == "" {
		return fmt.Errorf("want --auth-file, --cache-file or both")
	}
	s, err := server.New(opt)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.ImportNtfy(context.Background(), in, os.Stdout)
}

// health is for container health checks (distroless has no curl).
func health(opt server.Options) error {
	listen := opt.Listen
	if listen == "" {
		listen = ":8080"
	}
	c := http.Client{Timeout: 3 * time.Second}
	res, err := c.Get("http://127.0.0.1" + listen[strings.LastIndex(listen, ":"):] + "/healthz")
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("healthz returned %s", res.Status)
	}
	return nil
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
