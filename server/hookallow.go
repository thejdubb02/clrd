package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Outgoing webhooks may reach LAN addresses and ports other than 80 and 443
// only when an admin lists them (#237). Empty by default, so a webhook gets
// the favicon fetcher's guard: public addresses, ports 80 and 443.

type hookAllow struct {
	Networks []string `json:"networks"`
	Ports    []int    `json:"ports"`
	Locked   bool     `json:"locked"` // CLRD_HOOK_ALLOW=off: empty, and PUT refuses
	nets     []netip.Prefix
}

const hookAllowMax = 64

// permits reports whether a webhook may dial ip:port under the list: the
// address is public or in a listed network, and the port is 80, 443 or listed.
func (a *hookAllow) permits(ip netip.Addr, port string) bool {
	ip = ip.Unmap().WithZone("") // a prefix never contains a zoned address
	okIP := !blockedIP(ip) || slices.ContainsFunc(a.nets, func(p netip.Prefix) bool { return p.Contains(ip) })
	okPort := port == "80" || port == "443" || slices.ContainsFunc(a.Ports, func(p int) bool { return strconv.Itoa(p) == port })
	return okIP && okPort
}

// cleanHookAllow checks and normalises a list: a bare address becomes a
// single-address network, 192.168.1.7/24 becomes 192.168.1.0/24.
func cleanHookAllow(networks []string, ports []int) (*hookAllow, error) {
	if len(networks) > hookAllowMax || len(ports) > hookAllowMax {
		return nil, errf(400, "bad_request", "at most %d networks and %d ports", hookAllowMax, hookAllowMax)
	}
	a := &hookAllow{Networks: []string{}, Ports: []int{}}
	for _, n := range networks {
		n = strings.TrimSpace(n)
		p, err := netip.ParsePrefix(n)
		if err != nil {
			ip, err2 := netip.ParseAddr(n)
			if err2 != nil {
				return nil, errf(400, "bad_request", "network %q must be an address or a CIDR range, like 192.168.1.0/24", n)
			}
			ip = ip.WithZone("").Unmap()
			p = netip.PrefixFrom(ip, ip.BitLen())
		}
		if p.Addr().Is4In6() { // ::ffff:10.0.0.0/104 is 10.0.0.0/8
			if p.Bits() < 96 {
				return nil, errf(400, "bad_request", "network %q: write IPv4 ranges as IPv4, like 10.0.0.0/8", n)
			}
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		if p = p.Masked(); !slices.Contains(a.nets, p) {
			a.nets = append(a.nets, p)
			a.Networks = append(a.Networks, p.String())
		}
	}
	for _, p := range ports {
		if p < 1 || p > 65535 {
			return nil, errf(400, "bad_request", "port %d must be 1 to 65535", p)
		}
		if !slices.Contains(a.Ports, p) {
			a.Ports = append(a.Ports, p)
		}
	}
	return a, nil
}

// hookAllowList is the stored list, read once and kept until set.
func (s *Server) hookAllowList(ctx context.Context) (*hookAllow, error) {
	if a := s.hookAllow.Load(); a != nil {
		return a, nil
	}
	var raw string
	var in hookAllow
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'hook_allow'`).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &in)
	} else if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	a, err := cleanHookAllow(in.Networks, in.Ports)
	if err != nil {
		return nil, err
	}
	if !s.hookAllow.CompareAndSwap(nil, a) { // a save won the race
		return s.hookAllow.Load(), nil
	}
	return a, nil
}

// newHookClient is the webhook client: the fetcher's transport settings
// (no proxy) on a pool of its own, so a connection a hook opened to a
// listed LAN address is never reused by a favicon fetch. Each dial passes
// the fetcher's guard or the allow list; a list not yet read allows nothing
// extra, and sendHook reads it first.
func (s *Server) newHookClient() *http.Client {
	f := s.fetch
	d := &net.Dialer{Timeout: fetchTimeout, Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		port := address[strings.LastIndexByte(address, ':')+1:]
		if err == nil && (f.allow(ap.Addr(), port) || s.hookAllow.Load() != nil && s.hookAllow.Load().permits(ap.Addr(), port)) {
			return nil
		}
		return errBlocked
	}}
	t := f.client.Transport.(*http.Transport).Clone()
	t.DialContext = d.DialContext
	return &http.Client{Transport: t, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (s *Server) hookAllowRoutes() {
	get := func(w http.ResponseWriter, r *http.Request) error {
		a, err := s.hookAllowList(r.Context())
		if err != nil {
			return err
		}
		writeJSON(w, 200, a)
		return nil
	}
	s.api("GET /api/v1/hooks/allow", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.needFullAdmin(); err != nil {
			return err
		}
		return get(w, r)
	})
	s.api("PUT /api/v1/hooks/allow", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.needFullAdmin(); err != nil {
			return err
		}
		if s.hookAllow.Load() != nil && s.hookAllow.Load().Locked {
			return errf(403, "locked", "the server operator locked the webhook allow list (CLRD_HOOK_ALLOW=off); it stays empty")
		}
		var in struct {
			Networks *[]string `json:"networks"`
			Ports    *[]int    `json:"ports"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		if in.Networks == nil || in.Ports == nil { // a typo or {} must not change the list
			return errf(400, "bad_request", "networks and ports required: lists, or [] for none")
		}
		a, err := cleanHookAllow(*in.Networks, *in.Ports)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(a)
		s.mu.Lock() // two saves must store in the order they wrote
		_, err = s.db.ExecContext(r.Context(), `INSERT INTO settings (key, value) VALUES ('hook_allow', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(raw))
		if err == nil {
			s.hookAllow.Store(a)
			// A connection to an address just taken off the list, pooled or
			// still dialing, must not carry the next delivery: start a new pool.
			old := s.hookHTTP.Swap(s.newHookClient())
			old.Transport.(*http.Transport).CloseIdleConnections()
		}
		s.mu.Unlock()
		if err != nil {
			return err
		}
		return get(w, r)
	})
}
