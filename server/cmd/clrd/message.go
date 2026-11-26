package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/thejdubb02/clrd/server"
)

func messageCmd(ctx context.Context, opt server.Options, cmd string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	method, suffix, body := http.MethodGet, "/json", ""
	headers := http.Header{}
	q := url.Values{}
	if cmd == "publish" {
		title := fs.String("title", "", "notification title")
		priority := fs.String("priority", "", "1 to 5 or an ntfy priority name")
		tags := fs.String("tags", "", "comma-separated tags")
		if err := fs.Parse(args); err != nil {
			return err
		}
		method, suffix = http.MethodPut, ""
		headers.Set("X-Title", *title)
		headers.Set("X-Priority", *priority)
		headers.Set("X-Tags", *tags)
	} else {
		since := fs.String("since", "", "start from all, latest, a duration, unix time or message ID")
		if err := fs.Parse(args); err != nil {
			return err
		}
		q.Set("since", *since)
		if cmd == "poll" {
			q.Set("poll", "1")
		}
	}
	topic := fs.Arg(0)
	if topic == "" || (cmd != "publish" && fs.NArg() != 1) || topic == "." || topic == ".." {
		return fmt.Errorf("%s: expected a topic", cmd)
	}
	if cmd == "publish" {
		body = strings.Join(fs.Args()[1:], " ")
	}
	base := strings.TrimRight(os.Getenv("CLRD_URL"), "/")
	if base == "" {
		base = localURL(opt)
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("CLRD_URL must be an HTTP or HTTPS base URL without credentials, a query or a fragment")
	}
	target := base + "/" + url.PathEscape(topic) + suffix
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header = headers
	if token := os.Getenv("CLRD_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// A redirect can point at another server. Keep the token on the origin
	// the caller chose, and let them set CLRD_URL to the final address.
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if cmd != "subscribe" {
		client.Timeout = 10 * time.Second
	}
	res, err := client.Do(req)
	if err != nil {
		if cmd == "subscribe" && ctx.Err() == context.Canceled {
			return nil
		}
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: server returned %s", cmd, res.Status)
	}
	_, err = io.Copy(out, res.Body)
	if cmd == "subscribe" && ctx.Err() == context.Canceled {
		return nil
	}
	return err
}
