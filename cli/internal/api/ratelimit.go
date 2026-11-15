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

// allow reports whether client may start another upload and, if not, how long until it may.
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
	return true, 0
}

// record counts an upload of size bytes and reports whether it fit in the byte budget.
func (l *uploadLimiter) record(client string, size int64) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.current(client)
	if l.bytes > 0 && w.bytes+size > l.bytes {
		return false, w.start.Add(l.window).Sub(l.now())
	}
	w.uploads++
	w.bytes += size
	return true, 0
}

// clientIP identifies the uploader. Behind a trusted proxy it uses the last
// X-Forwarded-For entry, which the proxy appended; earlier entries are client-supplied.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if ip := strings.TrimSpace(xff[strings.LastIndexByte(xff, ',')+1:]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
