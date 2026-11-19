package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Outbound fetches for favicons and link previews (spec, Message
// formatting). Every connection is checked after DNS, on every redirect
// hop, so a public name that resolves to a private address is refused.

const (
	fetchTimeout  = 5 * time.Second
	fetchMax      = 1 << 20
	previewImgMax = 512 << 10
	faviconTTL    = 7 * 24 * time.Hour
	previewTTL    = 24 * time.Hour
	fetchUA       = "clrd/1 (+https://github.com/thejdubb02/clrd)"
)

// blockedNets are special-use ranges the netip helpers do not cover.
var blockedNets = func() []netip.Prefix {
	var ps []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"64:ff9b::/96", "64:ff9b:1::/48", "2001::/32", "2001:db8::/32", "2002::/16", "fc00::/7",
	} {
		ps = append(ps, netip.MustParsePrefix(s))
	}
	return ps
}()

// blockedIP reports whether a fetch may never reach ip: loopback, private,
// link-local (cloud metadata), multicast, unspecified and special-use.
func blockedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, p := range blockedNets {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

var errBlocked = errors.New("address not allowed")

type fetcher struct {
	dir    string
	client *http.Client
	sem    chan struct{}
	// allow decides each dial; tests swap it to reach httptest servers.
	allow func(ip netip.Addr, port string) bool
}

func newFetcher(dir string) *fetcher {
	f := &fetcher{dir: dir, sem: make(chan struct{}, 8)}
	f.allow = func(ip netip.Addr, port string) bool {
		return (port == "80" || port == "443") && !blockedIP(ip)
	}
	d := &net.Dialer{Timeout: fetchTimeout, Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !f.allow(ap.Addr(), address[strings.LastIndexByte(address, ':')+1:]) {
			return errBlocked
		}
		return nil
	}}
	f.client = &http.Client{
		Timeout: fetchTimeout,
		Transport: &http.Transport{
			Proxy:                 nil, // a proxy would dial for us, past the check
			DialContext:           d.DialContext,
			TLSHandshakeTimeout:   fetchTimeout,
			ResponseHeaderTimeout: fetchTimeout,
			MaxIdleConns:          8,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return checkURL(req.URL)
		},
	}
	return f
}

func checkURL(u *url.URL) error {
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return errf(400, "bad_request", "url must be http or https")
	}
	return nil
}

// get fetches u (at most fetchMax bytes) and returns the body and the URL
// it ended at after redirects.
func (f *fetcher) get(ctx context.Context, u *url.URL, max int64) ([]byte, *url.URL, error) {
	if err := checkURL(u); err != nil {
		return nil, nil, err
	}
	select {
	case f.sem <- struct{}{}:
		defer func() { <-f.sem }()
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", fetchUA)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, nil, errors.New(resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(b)) > max {
		return nil, nil, errors.New("too large")
	}
	return b, resp.Request.URL, nil
}

// rasterType sniffs b and returns its type if it is a raster image. SVG
// and anything else is refused: the bytes come from a stranger.
func rasterType(b []byte) string {
	switch t := http.DetectContentType(b); t {
	case "image/png", "image/gif", "image/jpeg", "image/webp", "image/x-icon", "image/bmp":
		return t
	}
	return ""
}

// The cache is one JSON file per key; its mtime is its age. Failures are
// cached too, so a dead host is not asked again on every render.
func (f *fetcher) cachePath(kind, key string) string {
	h := sha256.Sum256([]byte(kind + "\x00" + key))
	return filepath.Join(f.dir, kind+"-"+hex.EncodeToString(h[:16]))
}

func (f *fetcher) cached(kind, key string, ttl time.Duration, v any) bool {
	p := f.cachePath(kind, key)
	fi, err := os.Stat(p)
	if err != nil || time.Since(fi.ModTime()) > ttl {
		return false
	}
	b, err := os.ReadFile(p)
	return err == nil && json.Unmarshal(b, v) == nil
}

func (f *fetcher) store(kind, key string, v any) {
	b, _ := json.Marshal(v)
	if os.MkdirAll(f.dir, 0o750) != nil {
		return
	}
	tmp, err := os.CreateTemp(f.dir, "tmp-")
	if err != nil {
		return
	}
	_, err = tmp.Write(b)
	if tmp.Close() != nil || err != nil || os.Rename(tmp.Name(), f.cachePath(kind, key)) != nil {
		os.Remove(tmp.Name())
	}
}

// sweep deletes cache files older than the longest TTL. Runs hourly.
func (f *fetcher) sweep(context.Context) error {
	es, err := os.ReadDir(f.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range es {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > faviconTTL {
			os.Remove(filepath.Join(f.dir, e.Name()))
		}
	}
	return nil
}

var (
	reTag   = regexp.MustCompile(`(?is)<(meta|link)\s[^>]*>`)
	reAttr  = regexp.MustCompile(`(?is)([a-z:_-]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	reTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reHost  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?)*$`)
)

// tags returns the attributes of every <meta> and <link> tag in page.
func tags(page []byte) []map[string]string {
	var out []map[string]string
	for _, m := range reTag.FindAllSubmatch(page, 200) {
		a := map[string]string{"": strings.ToLower(string(m[1]))}
		for _, kv := range reAttr.FindAllSubmatch(m[0], -1) {
			a[strings.ToLower(string(kv[1]))] = html.UnescapeString(string(kv[2]) + string(kv[3]) + string(kv[4]))
		}
		out = append(out, a)
	}
	return out
}

type favicon struct {
	Type string `json:"type,omitempty"` // "" = failed, serve the fallback
	Body []byte `json:"body,omitempty"`
}

// favicon tries /favicon.ico, then the page's <link rel=icon>.
func (f *fetcher) favicon(ctx context.Context, host string) favicon {
	var fi favicon
	// A miss is cached 1 hour, so one timeout does not hide an icon for a week.
	if f.cached("icon", host, faviconTTL, &fi) && (fi.Type != "" || f.cached("icon", host, time.Hour, &fi)) {
		return fi
	}
	base := &url.URL{Scheme: "https", Host: host, Path: "/"}
	if b, _, err := f.get(ctx, base.ResolveReference(&url.URL{Path: "/favicon.ico"}), fetchMax); err == nil && rasterType(b) != "" {
		fi = favicon{rasterType(b), b}
	} else if page, final, err := f.get(ctx, base, fetchMax); err == nil {
		for _, t := range tags(page) {
			if t[""] != "link" || !strings.Contains(strings.ToLower(t["rel"]), "icon") || t["href"] == "" {
				continue
			}
			if ref, err := url.Parse(t["href"]); err == nil {
				if b, _, err := f.get(ctx, final.ResolveReference(ref), fetchMax); err == nil && rasterType(b) != "" {
					fi = favicon{rasterType(b), b}
					break
				}
			}
		}
	}
	f.store("icon", host, fi)
	return fi
}

// Preview is a link's Open Graph card. Image is a data: URL so the viewer
// never contacts the linked site.
type Preview struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	SiteName    string `json:"site_name,omitempty"`
	Image       string `json:"image,omitempty"`
}

func (f *fetcher) preview(ctx context.Context, u *url.URL) Preview {
	p := Preview{URL: u.String()}
	if f.cached("preview", p.URL, previewTTL, &p) {
		return p
	}
	page, final, err := f.get(ctx, u, fetchMax)
	if err == nil && strings.Contains(http.DetectContentType(page), "html") {
		var img string
		for _, t := range tags(page) {
			key := t["property"]
			if key == "" {
				key = t["name"]
			}
			v := strings.TrimSpace(t["content"])
			switch strings.ToLower(key) {
			case "og:title":
				p.Title = v
			case "og:description":
				p.Description = v
			case "description":
				if p.Description == "" {
					p.Description = v
				}
			case "og:site_name":
				p.SiteName = v
			case "og:image", "og:image:url":
				if img == "" {
					img = v
				}
			}
		}
		if m := reTitle.FindSubmatch(page); p.Title == "" && m != nil {
			p.Title = strings.TrimSpace(html.UnescapeString(string(m[1])))
		}
		p.Title, p.Description = clip(p.Title, 300), clip(p.Description, 1000)
		p.SiteName = clip(p.SiteName, 100)
		if ref, err := url.Parse(img); img != "" && err == nil {
			if b, _, err := f.get(ctx, final.ResolveReference(ref), previewImgMax); err == nil {
				if t := rasterType(b); t != "" {
					p.Image = "data:" + t + ";base64," + base64.StdEncoding.EncodeToString(b)
				}
			}
		}
	}
	f.store("preview", p.URL, p)
	return p
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// fallbackIcon is served when a site's icon cannot be fetched: a plain globe.
var fallbackIcon = []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="#888" stroke-width="1.5"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18"/></svg>`)

func (s *Server) fetchRoutes() {
	s.api("GET /api/v1/favicon", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.need(ScopeRead, ""); err != nil {
			return err
		}
		host := strings.ToLower(strings.TrimSuffix(r.URL.Query().Get("host"), "."))
		if len(host) > 253 || !reHost.MatchString(host) {
			return errf(400, "bad_request", "host must be a hostname")
		}
		fi := s.fetch.favicon(r.Context(), host)
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "sandbox")
		h.Set("Cache-Control", "private, max-age=86400")
		if fi.Type == "" {
			h.Set("Content-Type", "image/svg+xml")
			_, err := w.Write(fallbackIcon)
			return err
		}
		h.Set("Content-Type", fi.Type)
		_, err := w.Write(fi.Body)
		return err
	})
	s.api("GET /api/v1/preview", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		st, err := s.Settings(r.Context(), tok)
		if err != nil {
			return err
		}
		q := r.URL.Query()
		topic := q.Get("topic")
		if topic != "" && !tok.Allows(topic) {
			return errf(403, "forbidden", "token may not use topic %q", topic)
		}
		if !st.previewsOn(topic) {
			return errf(403, "forbidden", "link previews are off")
		}
		u, err := url.Parse(q.Get("url"))
		if err != nil {
			return errf(400, "bad_request", "bad url")
		}
		if err := checkURL(u); err != nil {
			return err
		}
		w.Header().Set("Cache-Control", "private, max-age=3600")
		writeJSON(w, 200, s.fetch.preview(r.Context(), u))
		return nil
	})
}
