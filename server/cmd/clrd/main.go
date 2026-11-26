// Command clrd runs the clrd server and manages its tokens.
//
//	clrd [serve]
//	clrd token create --name pixel-9 --scope read,publish [--topics a,b]
//	clrd token list
//	clrd agent invite --name bot --topics a,b [--expires 15m]
//	clrd import ntfy --auth-file user.db --cache-file cache.db --attachment-dir DIR --default-access deny-all [--clear-older-than 72h] [--dry-run]
//	clrd health
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
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
	case "agent":
		return agentCmd(opt, args)
	}
	return fmt.Errorf("unknown command %q (want serve, token, agent, import or health; see clrd help)", cmd)
}

const usage = `clrd: self-hosted notifications. Dismiss once, gone everywhere.

  clrd [serve]                 run the server (no settings needed)
  clrd token create [--name me] [--scope read,publish|admin] [--topics a,b] [--approve allowed|ask]
  clrd token list
  clrd agent invite --name bot --topics a,b [--expires 15m]   (CLRD_TOKEN: an admin token; CLRD_URL: the server)
  clrd import ntfy --auth-file user.db --cache-file cache.db --default-access deny-all [--dry-run]
  clrd import gotify --db gotify.db [--clear-older-than 720h] [--dry-run]
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
		approve := fs.String("approve", "denied", "allowed lets it answer approval requests (Allow on an agent prompt); ask lets it answer once an allowed device confirms")
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
				grants += " " + g.Topic + "=" + strings.Join(g.Scopes, "+")
			}
			fmt.Printf("%s\t%s\t%s\ttopics=%s\tgrants=%s\tseen=%s\n", t.ID, t.Name, strings.Join(t.Scopes, ","), strings.Join(t.Topics, ","), strings.TrimSpace(grants), seen)
		}
	default:
		return fmt.Errorf("unknown token command %q", args[0])
	}
	return nil
}

// importCmd copies another server's data in: ntfy (issue #117) or Gotify (#116).
func importCmd(opt server.Options, args []string) error {
	if len(args) > 0 && args[0] == "gotify" {
		return importGotify(opt, args[1:])
	}
	if len(args) == 0 || args[0] != "ntfy" {
		return fmt.Errorf("want import ntfy or import gotify")
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

func importGotify(opt server.Options, args []string) error {
	fs := flag.NewFlagSet("import gotify", flag.ContinueOnError)
	var in server.GotifyImport
	fs.StringVar(&in.DB, "db", "", "Gotify's database (data/gotify.db)")
	fs.DurationVar(&in.ClearOlderThan, "clear-older-than", 0, "messages older than this arrive dismissed (0 keeps all open)")
	fs.BoolVar(&in.DryRun, "dry-run", false, "print what maps and what does not, change nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if in.DB == "" {
		return fmt.Errorf("want --db gotify.db")
	}
	s, err := server.New(opt)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.ImportGotify(context.Background(), in, os.Stdout)
}

// health is for container health checks (distroless has no curl).
// agentCmd makes an agent invite (#454) on the running server, since codes
// live in its memory: CLRD_TOKEN is an admin token, CLRD_URL the server
// (default this box's listen address).
func agentCmd(opt server.Options, args []string) error {
	if len(args) == 0 || args[0] != "invite" {
		return fmt.Errorf("want agent invite")
	}
	fs := flag.NewFlagSet("agent invite", flag.ContinueOnError)
	name := fs.String("name", "", "the agent's name, shown in the user and device lists")
	topics := fs.String("topics", "", "comma list of topics it may read and publish")
	expires := fs.String("expires", "", "how long the invite lasts, 1m to 1h (default 15m)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	base := strings.TrimRight(os.Getenv("CLRD_URL"), "/")
	if base == "" {
		base = localURL(opt)
	}
	body, _ := json.Marshal(map[string]any{"name": *name, "topics": split(*topics), "expires_in": *expires})
	req, err := http.NewRequest("POST", base+"/api/v1/agents/invite", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("CLRD_TOKEN"))
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var out struct {
		Invite    string    `json:"invite"`
		URL       string    `json:"url"`
		ExpiresAt time.Time `json:"expires_at"`
		Error     string    `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || res.StatusCode != 201 {
		return fmt.Errorf("invite: %s %s", res.Status, out.Error)
	}
	fmt.Fprintf(os.Stderr, "Single use, expires %s. Hand it over privately:\n", out.ExpiresAt.Format(time.RFC3339))
	fmt.Println(out.Invite)
	fmt.Println(out.URL)
	return nil
}

// localURL is this box's server, from its listen address.
func localURL(opt server.Options) string {
	listen := cmp.Or(opt.Listen, ":8080")
	return "http://127.0.0.1" + listen[strings.LastIndex(listen, ":"):]
}

func health(opt server.Options) error {
	c := http.Client{Timeout: 3 * time.Second}
	res, err := c.Get(localURL(opt) + "/healthz")
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
