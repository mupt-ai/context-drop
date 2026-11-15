package api

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// uploadLimiter caps anonymous uploads per client over a fixed window, by
// count and by bytes. A zero limit disables that check.
type uploadLimiter struct {
	uploads int
	bytes   int64
	window  time.Duration
	now     func() time.Time

	mu      sync.Mutex
	clients map[string]*uploadWindow
	swept   time.Time
}

type uploadWindow struct {
	start   time.Time
	uploads int
	bytes   int64
}

func newUploadLimiter(uploads int, bytes int64, window time.Duration) *uploadLimiter {
	if uploads <= 0 && bytes <= 0 {
		return nil
	}
	if window <= 0 {
		window = time.Hour
	}
	return &uploadLimiter{uploads: uploads, bytes: bytes, window: window, now: time.Now, clients: map[string]*uploadWindow{}}
}

// current returns the client's window, starting a new one when the old has ended.
func (l *uploadLimiter) current(client string) *uploadWindow {
	now := l.now()
	if now.Sub(l.swept) > l.window {
		for k, w := range l.clients {
			if now.Sub(w.start) >= l.window {
				delete(l.clients, k)
			}
		}
		l.swept = now
	}
	w := l.clients[client]
	if w == nil || now.Sub(w.start) >= l.window {
		w = &uploadWindow{start: now}
		l.clients[client] = w
	}
	return w
}

// allow reports whether client may start another upload and, if not, how long
// until it may. An allowed upload is counted at once, so concurrent requests
// cannot all slip under the count.
func (l *uploadLimiter) allow(client string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.current(client)
	if (l.uploads > 0 && w.uploads >= l.uploads) || (l.bytes > 0 && w.bytes >= l.bytes) {
		return false, w.start.Add(l.window).Sub(l.now())
	}
	w.uploads++
	return true, 0
}

// record charges size bytes, the body already read, and reports whether they
// fit in the byte budget. Rejected bodies are charged too, so a client cannot
// keep sending oversized uploads for free.
func (l *uploadLimiter) record(client string, size int64) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.current(client)
	w.bytes += size
	if l.bytes > 0 && w.bytes > l.bytes {
		return false, w.start.Add(l.window).Sub(l.now())
	}
	return true, 0
}

// clientIP identifies the uploader. Behind proxyHops trusted proxies it takes
// the X-Forwarded-For entry that many places from the right: each proxy appends
// one entry, and anything further left is client-supplied. IPv6 clients are
// keyed by their /64, since one host can hold many addresses in it.
func clientIP(r *http.Request, proxyHops int) string {
	ip := ""
	if proxyHops > 0 {
		entries := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if len(entries) >= proxyHops {
			ip = strings.TrimSpace(entries[len(entries)-proxyHops])
		}
	}
	if ip == "" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip = host
	}
	if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() == nil {
		return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
	}
	return ip
}
