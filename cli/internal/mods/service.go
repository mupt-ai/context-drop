package mods

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Capabilities a mod may list in `can`.
var capabilities = map[string]bool{"list": true, "read": true, "send": true, "call": true, "kv": true}

// Planned capabilities and events this server does not offer yet.
var notYet = map[string]bool{"ask": true, "summarize": true, "files:read": true, "files:write": true}

// Caller is who a mod call is for.
type Caller struct {
	Kind    string `json:"kind"` // "machine", "agent", "event", or "mod"
	Machine string `json:"machine,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Mod     string `json:"mod,omitempty"`
}

// Event is something that happened in the org, delivered to the mods whose `on` names it.
type Event map[string]any

// Grant is what a running mod's token allows.
type Grant struct {
	Org   string
	Mod   string
	Can   map[string]bool
	Depth int
}

type token struct {
	Grant
	expires time.Time
}

// LogEntry records one call.
type LogEntry struct {
	Time    time.Time `json:"time"`
	Version int       `json:"version"`
	Caller  Caller    `json:"caller"`
	Event   string    `json:"event,omitempty"`
	Millis  int64     `json:"ms"`
	OK      bool      `json:"ok"`
	Error   string    `json:"error,omitempty"`
	Output  string    `json:"output,omitempty"` // what the mod printed
}

// Service deploys, lists, and runs an org's mods.
type Service struct {
	store *Store
	rt    *Runtime
	log   *slog.Logger
	// APIBase is the URL mods use to call back into this server, e.g. http://127.0.0.1:8080.
	APIBase string
	// Timeout bounds one call (default 15 minutes).
	Timeout time.Duration

	mu     sync.Mutex
	tokens map[string]token
	logs   map[string][]LogEntry // org/name → newest last
}

// NewService runs mods from store with rt.
func NewService(store *Store, rt *Runtime, apiBase string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: store, rt: rt, log: log, APIBase: apiBase, Timeout: 15 * time.Minute,
		tokens: map[string]token{}, logs: map[string][]LogEntry{}}
}

// Error is a failure with a code the API returns as is.
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }

func errf(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// check validates a mod's metadata and returns its outbound hosts.
func check(m Meta) ([]string, error) {
	if !ValidName(m.Name) {
		return nil, errf("bad_request", "mod name %q: use lowercase letters, digits, and dashes, starting with a letter", m.Name)
	}
	var hosts []string
	for _, c := range m.Can {
		switch {
		case capabilities[c]:
		case strings.HasPrefix(c, "secret:") && len(c) > len("secret:"):
		case strings.HasPrefix(c, "net:") && len(c) > len("net:"):
			host := strings.TrimPrefix(c, "net:")
			if strings.ContainsAny(host, "/ ,") {
				return nil, errf("bad_request", "can %q: name a host, like net:api.github.com", c)
			}
			if !publicHost(host) {
				return nil, errf("bad_request", "can %q: a mod may reach only public hosts, not this server's own network", c)
			}
			hosts = append(hosts, host)
		case notYet[c]:
			return nil, errf("unsupported", "can %q is not available on this server yet", c)
		default:
			return nil, errf("bad_request", "unknown capability %q (use list, read, send, call, kv, secret:<name>, net:<host>)", c)
		}
	}
	for _, on := range m.On {
		switch {
		case on == "agent.started" || on == "agent.ended" || on == "agent.status":
		case on == "webhook":
			if m.Webhook == nil {
				return nil, errf("bad_request", `on "webhook" needs webhook: { verify: "github" | "slack" | "hmac" }`)
			}
		case strings.HasPrefix(on, "schedule:"):
			if _, err := parseCron(strings.TrimPrefix(on, "schedule:")); err != nil {
				return nil, errf("bad_request", "%v", err)
			}
		case on == "agent.entry" || strings.HasPrefix(on, "file:"):
			return nil, errf("unsupported", "on %q is not available on this server yet", on)
		default:
			return nil, errf("bad_request", "unknown event %q (use agent.started, agent.ended, agent.status, schedule:<cron>, webhook)", on)
		}
	}
	if m.Webhook != nil {
		switch m.Webhook.Verify {
		case "github", "slack", "hmac":
		default:
			return nil, errf("bad_request", "webhook.verify must be github, slack, or hmac")
		}
	}
	return hosts, nil
}

// DeployResult describes a deploy, or what one would do.
type DeployResult struct {
	Name    string   `json:"name"`
	Version int      `json:"version"`
	Can     []string `json:"can"`
	Added   []string `json:"added,omitempty"` // capabilities the previous version did not have
	Preview bool     `json:"preview,omitempty"`
	Webhook string   `json:"webhook,omitempty"` // the path of its webhook, when it has one
}

// Deploy checks source and stores it as a new version. A preview is deployed as name@machine, callable
// only from that machine. With dryRun it reports what the deploy would do without storing it.
func (s *Service) Deploy(ctx context.Context, org, machine, author, source string, preview, dryRun bool) (DeployResult, error) {
	if len(source) > 1<<20 {
		return DeployResult{}, errf("bad_request", "a mod is one file of at most 1 MB")
	}
	meta, err := s.rt.Describe(ctx, source)
	if err != nil {
		return DeployResult{}, errf("bad_request", "%v", err)
	}
	if _, err := check(meta); err != nil {
		return DeployResult{}, err
	}
	name, owner := meta.Name, ""
	if preview {
		name, owner = meta.Name+"@"+machine, machine
	}
	res := DeployResult{Name: name, Can: meta.Can, Preview: preview}
	prev := map[string]bool{}
	if m, _, err := s.store.Get(org, name); err == nil {
		res.Version = m.Versions[len(m.Versions)-1].Version + 1
		if cur := m.Current(); cur != nil {
			for _, c := range cur.Can {
				prev[c] = true
			}
		}
	} else {
		res.Version = 1
	}
	for _, c := range meta.Can {
		if !prev[c] {
			res.Added = append(res.Added, c)
		}
	}
	if meta.Webhook != nil && !preview {
		res.Webhook = "/hooks/" + org + "/" + name
	}
	if dryRun {
		return res, nil
	}
	v, err := s.store.Put(org, name, owner, meta, source, author)
	if err != nil {
		return DeployResult{}, err
	}
	res.Version = v.Version
	s.log.Info("mod deployed", "org", org, "mod", name, "version", v.Version, "author", author)
	return res, nil
}

// Summary is one row of `context-drop mods`.
type Summary struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Version     int             `json:"version"`
	Can         []string        `json:"can"`
	On          []string        `json:"on,omitempty"`
	Input       json.RawMessage `json:"input"`
	Author      string          `json:"author"`
	Deployed    time.Time       `json:"deployed"`
	Preview     bool            `json:"preview,omitempty"`
}

// List returns the mods machine can see: the org's, plus its own previews.
func (s *Service) List(org, machine string) ([]Summary, error) {
	mods, err := s.store.List(org)
	if err != nil {
		return nil, err
	}
	out := []Summary{}
	for _, m := range mods {
		cur := m.Current()
		if cur == nil || (m.Owner != "" && m.Owner != machine) {
			continue
		}
		out = append(out, Summary{Name: m.Name, Description: cur.Description, Version: cur.Version, Can: cur.Can, On: cur.On,
			Input: cur.Input, Author: cur.Author, Deployed: cur.Created, Preview: m.Owner != ""})
	}
	return out, nil
}

func (s *Service) visible(org, name, machine string) (*Mod, string, error) {
	m, src, err := s.store.Get(org, name)
	if errors.Is(err, ErrNotFound) || (err == nil && m.Owner != "" && m.Owner != machine) {
		return nil, "", errf("not_found", "org %s has no mod %q", org, name)
	}
	return m, src, err
}

// Rollback restores the previous version.
func (s *Service) Rollback(org, name, machine string) (int, error) {
	if _, _, err := s.visible(org, name, machine); err != nil {
		return 0, err
	}
	return s.store.Rollback(org, name)
}

// SetSecret sets a secret a mod reads with secrets.get when it lists secret:<key>.
func (s *Service) SetSecret(org, name, machine, key, value string) error {
	if _, _, err := s.visible(org, name, machine); err != nil {
		return err
	}
	if key == "" || strings.ContainsAny(key, " /") {
		return errf("bad_request", "secret name %q is not valid", key)
	}
	return s.store.SetSecret(org, name, key, value)
}

// Logs returns a mod's recent calls, newest first.
func (s *Service) Logs(org, name, machine string) ([]LogEntry, error) {
	if _, _, err := s.visible(org, name, machine); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.logs[org+"/"+name]
	out := make([]LogEntry, len(src))
	for i, e := range src {
		out[len(src)-1-i] = e
	}
	return out, nil
}

// Grant returns what a mod token allows, if it is live.
func (s *Service) Grant(tok string) (Grant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[tok]
	if !ok || time.Now().After(t.expires) {
		delete(s.tokens, tok)
		return Grant{}, false
	}
	return t.Grant, true
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "cdx_" + hex.EncodeToString(b)
}

// Call runs a mod. machine is who may see previews; depth counts nested mod-to-mod calls.
func (s *Service) Call(ctx context.Context, org, name, machine string, input json.RawMessage, caller Caller, event Event, depth int) (json.RawMessage, error) {
	if depth > 4 {
		return nil, errf("bad_request", "mods called each other more than 4 deep")
	}
	m, src, err := s.visible(org, name, machine)
	if err != nil {
		return nil, err
	}
	cur := m.Current()
	hosts, _ := check(cur.Meta)
	can := map[string]bool{}
	secrets := map[string]string{}
	all := s.store.Secrets(org, name)
	for _, c := range cur.Can {
		can[c] = true
		if key, ok := strings.CutPrefix(c, "secret:"); ok {
			if v, ok := all[key]; ok {
				secrets[key] = v
			}
		}
	}
	if cur.Webhook != nil { // the webhook secret is the mod's own
		if v, ok := all["webhook"]; ok {
			secrets["webhook"] = v
		}
	}
	tok := newToken()
	s.mu.Lock()
	s.tokens[tok] = token{Grant: Grant{Org: org, Mod: name, Can: can, Depth: depth}, expires: time.Now().Add(s.Timeout + time.Minute)}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.tokens, tok)
		s.mu.Unlock()
	}()
	api, _ := url.Parse(s.APIBase)
	allow := append([]string{api.Host}, hosts...)
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	var ev any
	if event != nil {
		ev = event
	}
	started := time.Now()
	out, logs, err := s.rt.exec(ctx, src, invocation{Mode: "run", Input: input, Event: ev, Caller: caller,
		API: &API{Base: s.APIBase, Org: org, Token: tok}, Secrets: secrets}, allow, s.Timeout)
	entry := LogEntry{Time: started.UTC(), Version: cur.Version, Caller: caller, Millis: time.Since(started).Milliseconds(), OK: err == nil, Output: tail(logs, 4000)}
	if event != nil {
		entry.Event, _ = event["type"].(string)
	}
	if err != nil {
		entry.Error = err.Error()
	}
	s.mu.Lock()
	k := org + "/" + name
	s.logs[k] = append(s.logs[k], entry)
	if n := len(s.logs[k]); n > 100 {
		s.logs[k] = s.logs[k][n-100:]
	}
	s.mu.Unlock()
	var modErr *ErrMod
	if errors.As(err, &modErr) {
		return nil, errf("mod_failed", "%s: %s", name, modErr.Message)
	}
	return out, err
}

// KV runs a key-value operation for the mod a token belongs to.
func (s *Service) KV(g Grant, op, key string, value, expected json.RawMessage) (json.RawMessage, bool, error) {
	return s.store.KV(g.Org, g.Mod, op, key, value, expected)
}

func newEventID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Dispatch delivers an event to every mod in the org whose `on` names its type. Each gets one retry.
func (s *Service) Dispatch(org string, ev Event) {
	typ, _ := ev["type"].(string)
	if _, ok := ev["id"]; !ok {
		ev["id"] = newEventID()
	}
	mods, err := s.store.List(org)
	if err != nil {
		return
	}
	for _, m := range mods {
		cur := m.Current()
		if cur == nil || m.Owner != "" {
			continue
		}
		for _, on := range cur.On {
			if on == typ {
				go s.deliver(org, m.Name, ev)
				break
			}
		}
	}
}

func (s *Service) deliver(org, name string, ev Event) {
	for attempt := 0; attempt < 2; attempt++ {
		_, err := s.Call(context.Background(), org, name, "", nil, Caller{Kind: "event"}, ev, 0)
		if err == nil {
			return
		}
		s.log.Warn("mod event failed", "org", org, "mod", name, "event", ev["type"], "err", err)
		time.Sleep(5 * time.Second)
	}
}

// RunSchedules calls `schedule:<cron>` mods every minute (UTC) until ctx ends. orgs lists the orgs.
func (s *Service) RunSchedules(ctx context.Context, orgs func() []string) {
	for {
		now := time.Now().UTC()
		next := now.Truncate(time.Minute).Add(time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-time.After(next.Sub(now)):
		}
		for _, org := range orgs() {
			mods, _ := s.store.List(org)
			for _, m := range mods {
				cur := m.Current()
				if cur == nil || m.Owner != "" {
					continue
				}
				for _, on := range cur.On {
					spec, ok := strings.CutPrefix(on, "schedule:")
					if !ok {
						continue
					}
					if c, err := parseCron(spec); err == nil && c.matches(next) {
						go s.deliver(org, m.Name, Event{"id": newEventID(), "type": "schedule", "cron": spec, "time": next.Format(time.RFC3339)})
					}
				}
			}
		}
	}
}

// Webhook verifies an outside request against the mod's webhook secret and calls the mod with it.
func (s *Service) Webhook(ctx context.Context, org, name string, r *http.Request) (int, []byte) {
	m, _, err := s.store.Get(org, name)
	if err != nil || m.Owner != "" {
		return http.StatusNotFound, []byte("not found\n")
	}
	cur := m.Current()
	if cur == nil || cur.Webhook == nil {
		return http.StatusNotFound, []byte("not found\n")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return http.StatusBadRequest, nil
	}
	secret := s.store.Secrets(org, name)["webhook"]
	if secret == "" {
		return http.StatusServiceUnavailable, []byte("this webhook has no secret yet: context-drop secret set " + name + " webhook\n")
	}
	if !verify(cur.Webhook.Verify, secret, r.Header, body) {
		return http.StatusUnauthorized, []byte("bad signature\n")
	}
	headers := map[string]string{}
	for k := range r.Header {
		if lk := strings.ToLower(k); lk != "authorization" && lk != "cookie" {
			headers[lk] = r.Header.Get(k)
		}
	}
	var parsed any = string(body)
	var js any
	if json.Unmarshal(body, &js) == nil {
		parsed = js
	}
	id := r.Header.Get("X-GitHub-Delivery")
	if id == "" {
		id = newEventID()
	}
	out, err := s.Call(ctx, org, name, "", nil, Caller{Kind: "event"}, Event{"id": id, "type": "webhook", "headers": headers, "body": parsed}, 0)
	if err != nil {
		return http.StatusBadGateway, []byte(err.Error() + "\n")
	}
	var str string
	if json.Unmarshal(out, &str) == nil {
		return http.StatusOK, []byte(str)
	}
	return http.StatusOK, out
}

func hmacHex(secret, msg string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func verify(kind, secret string, h http.Header, body []byte) bool {
	mac := func(msg []byte) string { return hmacHex(secret, string(msg)) }
	switch kind {
	case "github":
		return hmac.Equal([]byte(h.Get("X-Hub-Signature-256")), []byte("sha256="+mac(body)))
	case "hmac":
		return hmac.Equal([]byte(h.Get("X-Signature-256")), []byte("sha256="+mac(body)))
	case "slack":
		ts := h.Get("X-Slack-Request-Timestamp")
		sec, err := strconv.ParseInt(ts, 10, 64)
		if err != nil || time.Since(time.Unix(sec, 0)).Abs() > 5*time.Minute {
			return false
		}
		return hmac.Equal([]byte(h.Get("X-Slack-Signature")), []byte("v0="+mac([]byte("v0:"+ts+":"+string(body)))))
	}
	return false
}

// AgentEvents compares a machine's previous and current agent lists and returns the events between them.
func AgentEvents(before, after []map[string]any) []Event {
	byID := func(list []map[string]any) map[string]map[string]any {
		m := map[string]map[string]any{}
		for _, a := range list {
			if id, ok := a["id"].(string); ok {
				m[id] = a
			}
		}
		return m
	}
	old, cur := byID(before), byID(after)
	var out []Event
	ids := make([]string, 0, len(cur))
	for id := range cur {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a := cur[id]
		prev, existed := old[id]
		switch {
		case !existed:
			out = append(out, Event{"type": "agent.started", "agent": a})
		case prev["status"] != a["status"]:
			out = append(out, Event{"type": "agent.status", "agent": a, "from": prev["status"], "to": a["status"]})
		}
	}
	gone := []string{}
	for id := range old {
		if _, ok := cur[id]; !ok {
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	for _, id := range gone {
		out = append(out, Event{"type": "agent.ended", "agent": old[id]})
	}
	return out
}

// publicHost reports whether a net: host is a public name or address. Loopback,
// private, link-local (the cloud metadata server), and internal names are refused,
// so a mod cannot reach the server's own network.
func publicHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]."))
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() || isShared(ip))
	}
	if !strings.Contains(host, ".") || host == "localhost" {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".lan", ".home.arpa"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	return true
}

// isShared is the carrier-grade NAT range (100.64.0.0/10), used inside some clouds.
func isShared(ip netip.Addr) bool {
	return netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}
