package server

import (
	"math"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// limiter is a token bucket per key: burst tokens, one more every `every`.
type limiter struct {
	mu    sync.Mutex
	burst float64
	every time.Duration
	m     map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter(burst int, every time.Duration) *limiter {
	return &limiter{burst: float64(burst), every: every, m: map[string]*bucket{}}
}

// take spends one token for key, or returns how long until one is free.
func (l *limiter) take(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.m[key]
	if b == nil {
		if len(l.m) >= 10000 { // ponytail: drop full buckets when big, an LRU if this ever churns
			for k, v := range l.m {
				if v.tokens+float64(now.Sub(v.at))/float64(l.every) >= l.burst {
					delete(l.m, k)
				}
			}
		}
		if len(l.m) >= 10000 {
			key = "(overflow)" // still full: new keys share one bucket rather than grow the map
			b = l.m[key]
		}
		if b == nil {
			b = &bucket{tokens: l.burst, at: now}
			l.m[key] = b
		}
	}
	if now.After(b.at) { // a racing caller with an older now must not turn the clock back
		b.tokens = math.Min(l.burst, b.tokens+float64(now.Sub(b.at))/float64(l.every))
		b.at = now
	}
	if b.tokens < 1 {
		return time.Duration((1 - b.tokens) * float64(l.every))
	}
	b.tokens--
	return 0
}

// give returns a token taken by take (a check that turned out fine).
func (l *limiter) give(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.m[key]; !ok {
		key = "(overflow)"
	}
	if b := l.m[key]; b != nil {
		b.tokens = math.Min(l.burst, b.tokens+1)
	}
}

// clientIP is the caller's address. Behind a trusted proxy it is the
// rightmost X-Forwarded-For hop, the one that proxy added. Trusted is the
// trusted list, or with none set any loopback or private-network peer.
// IPv6 is cut to its /64.
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && isTrustedProxy(ip, trusted) {
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			hops := strings.Split(xff[len(xff)-1], ",")
			if p := net.ParseIP(strings.TrimSpace(hops[len(hops)-1])); p != nil {
				ip = p
			}
		}
	}
	if ip == nil {
		return host
	}
	if ip.To4() == nil {
		return ip.Mask(net.CIDRMask(64, 128)).String()
	}
	return ip.String()
}

func isTrustedProxy(ip net.IP, trusted []netip.Prefix) bool {
	if trusted == nil {
		return ip.IsLoopback() || ip.IsPrivate()
	}
	a, _ := netip.AddrFromSlice(ip)
	for _, p := range trusted {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

// retryAfter returns the 429; Server.fail turns wait into Retry-After.
func retryAfter(wait time.Duration, msg string) error {
	return &apiError{status: 429, code: "rate_limited", msg: msg, retry: wait}
}

// perMinute is n a minute with a burst of n, or nil when n < 0 (off).
func perMinute(n int) *limiter {
	if n < 0 {
		return nil
	}
	return newLimiter(n, time.Minute/time.Duration(n))
}

func (s *Server) openNtfySubscription(ip string) bool {
	s.ntfySubsMu.Lock()
	defer s.ntfySubsMu.Unlock()
	if s.ntfySubs[ip] >= s.opt.NtfyMaxSubscriptions {
		return false
	}
	s.ntfySubs[ip]++
	return true
}

func (s *Server) closeNtfySubscription(ip string) {
	s.ntfySubsMu.Lock()
	defer s.ntfySubsMu.Unlock()
	if s.ntfySubs[ip] == 1 {
		delete(s.ntfySubs, ip)
	} else {
		s.ntfySubs[ip]--
	}
}

// publishLimit spends one publish from the token's and the topic's caps.
// ponytail: anonymous publishers share one bucket; key them by address if that bites.
func (s *Server) publishLimit(tok *Token, topic string) error {
	now := time.Now()
	if s.tokLimit != nil {
		if wait := s.tokLimit.take(tok.ID, now); wait > 0 {
			return retryAfter(wait, "this token is publishing too fast, try again later")
		}
	}
	if err := s.topicTake(topic); err != nil {
		if s.tokLimit != nil {
			s.tokLimit.give(tok.ID)
		}
		return err
	}
	return nil
}

// topicTake spends only the topic's allowance: a route target, whose
// sender already paid for the publish under the topic it named.
func (s *Server) topicTake(topic string) error {
	if s.topicLimit != nil {
		if wait := s.topicLimit.take(topic, time.Now()); wait > 0 {
			return retryAfter(wait, "this topic is getting too many messages, try again later")
		}
	}
	return nil
}
