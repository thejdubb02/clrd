// Command clrd runs the clrd server and manages its tokens.
//
//	clrd [serve]
//	clrd token create --name pixel-9 --scope read,publish [--topics a,b]
//	clrd token list
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
	case "serve":
		s, err := server.New(opt)
		if err != nil {
			return err
		}
		defer s.Close()
		s.Mount("/mcp", mcp.New(s))
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return s.Run(ctx)
	case "token":
		return tokenCmd(opt, args)
	case "health":
		return health(opt)
	}
	return fmt.Errorf("unknown command %q (want serve, token or health)", cmd)
}

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
		name := fs.String("name", "", "device name, shown as state_by")
		scope := fs.String("scope", "read,publish", "comma list of read, publish, admin")
		topics := fs.String("topics", "", "optional comma list of allowed topics")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		t, raw, err := s.CreateToken(context.Background(), *name, split(*scope), split(*topics))
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "created %s (%s), scopes %s. Shown once:\n", t.Name, t.ID, strings.Join(t.Scopes, ","))
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
			fmt.Printf("%s\t%s\t%s\ttopics=%s\tseen=%s\n", t.ID, t.Name, strings.Join(t.Scopes, ","), strings.Join(t.Topics, ","), seen)
		}
	default:
		return fmt.Errorf("unknown token command %q", args[0])
	}
	return nil
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
