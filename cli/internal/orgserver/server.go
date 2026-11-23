package orgserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"contextdrop.dev/context-drop/internal/mods"
	"contextdrop.dev/context-drop/internal/pool"
)

// Server serves the org endpoints.
type Server struct {
	store      *Store
	adminToken string
	log        *slog.Logger
	// CallTimeout bounds one relayed call to a machine (default 60s).
	CallTimeout time.Duration
	// Mods runs the org's mods; nil turns mods off.
	Mods *mods.Service
	// Slack is the server's Slack app; nil turns Slack off.
	Slack *SlackApp

	slack    slackState
	mu       sync.Mutex
	machines map[key]*machine
	seq      atomic.Uint64
}

type key struct{ org, name string }

// machine is a joined machine's live state: its connection, if any, and the
// agents it last reported.
type machine struct {
	conn    *websocket.Conn
	pending map[string]chan pool.Frame
	agents  []pool.Agent
	seen    time.Time
}

// New makes a server. adminToken guards org creation; empty disables it.
func New(store *Store, adminToken string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{store: store, adminToken: adminToken, log: log, machines: map[key]*machine{}}
}

// Register adds the org routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/orgs", s.handleCreateOrg)
	mux.HandleFunc("POST /v1/orgs/{org}/join-token", s.handleRotateToken)
	mux.HandleFunc("POST /v1/join", s.handleJoin)
	mux.HandleFunc("GET /v1/{org}/connect", s.authed(s.handleConnect))
	mux.HandleFunc("POST /v1/{org}/leave", s.authed(s.handleLeave))
	mux.HandleFunc("POST /v1/{org}/list", s.authed(s.handleList))
	mux.HandleFunc("POST /v1/{org}/read", s.authed(s.handleRead))
	mux.HandleFunc("POST /v1/{org}/send", s.authed(s.handleSend))
	mux.HandleFunc("POST /v1/{org}/ask", s.authed(s.handleAsk))
	mux.HandleFunc("POST /v1/{org}/mods", s.authed(s.handleMods))
	mux.HandleFunc("POST /v1/{org}/mods/deploy", s.authed(s.handleDeploy))
	mux.HandleFunc("POST /v1/{org}/mods/{name}/call", s.authed(s.handleCall))
	mux.HandleFunc("POST /v1/{org}/kv", s.authed(s.handleKV))
	s.registerSlack(mux)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]*pool.Error{"error": {Code: code, Message: msg}})
}

func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrExists):
		writeError(w, http.StatusConflict, "exists", err.Error())
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, ErrBadName):
		writeError(w, http.StatusBadRequest, "bad_name", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "failed", err.Error())
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func bearer(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) bool {
	if s.adminToken == "" || subtle.ConstantTimeCompare([]byte(bearer(r)), []byte(s.adminToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "org administration needs the server's admin token")
		return false
	}
	return true
}

func (s *Server) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	token, err := s.store.CreateOrg(in.Name)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"org": in.Name, "join_token": token})
}

func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	token, err := s.store.RotateJoinToken(r.PathValue("org"))
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"org": r.PathValue("org"), "join_token": token})
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token   string `json:"token"`
		Machine string `json:"machine"`
	}
	if !decode(w, r, &in) {
		return
	}
	org, cred, err := s.store.Join(in.Token, in.Machine)
	if err != nil {
		storeError(w, err)
		return
	}
	s.log.Info("machine joined", "org", org, "machine", in.Machine)
	writeJSON(w, http.StatusCreated, pool.JoinResponse{Org: org, Machine: in.Machine, Credential: cred})
}

// caller is who made a request: a machine, or a running mod with the capabilities its token grants.
type caller struct {
	org, machine string
	mod          *mods.Grant
}

// may reports whether the caller may use a capability; machines may use them all.
func (c caller) may(capability string) bool { return c.mod == nil || c.mod.Can[capability] }

// authed admits a request carrying a credential for the org in the path: a machine's, or a running mod's.
func (s *Server) authed(h func(http.ResponseWriter, *http.Request, caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if strings.HasPrefix(tok, "cdx_") && s.Mods != nil {
			if g, ok := s.Mods.Grant(tok); ok && g.Org == r.PathValue("org") {
				h(w, r, caller{org: g.Org, mod: &g})
				return
			}
		} else if m, ok := s.store.Authenticate(tok); ok && m.Org == r.PathValue("org") {
			h(w, r, caller{org: m.Org, machine: m.Name})
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "this credential is not a member of org "+r.PathValue("org"))
	}
}

// machinesOnly refuses mods, for endpoints only a machine may use.
func machinesOnly(w http.ResponseWriter, c caller) bool {
	if c.mod != nil {
		writeError(w, http.StatusForbidden, "forbidden", "mods cannot use this endpoint")
		return false
	}
	return true
}

// allowed refuses a mod whose can list lacks capability.
func allowed(w http.ResponseWriter, c caller, capability string) bool {
	if !c.may(capability) {
		writeError(w, http.StatusForbidden, "forbidden", fmt.Sprintf("mod %s needs %q in its can list", c.mod.Mod, capability))
		return false
	}
	return true
}

func (s *Server) handleLeave(w http.ResponseWriter, r *http.Request, c caller) {
	if !machinesOnly(w, c) {
		return
	}
	if err := s.store.Leave(c.org, c.machine); err != nil {
		storeError(w, err)
		return
	}
	s.mu.Lock()
	m := s.machines[key{c.org, c.machine}]
	delete(s.machines, key{c.org, c.machine})
	s.mu.Unlock()
	if m != nil && m.conn != nil {
		m.conn.Close(websocket.StatusPolicyViolation, "left the org")
	}
	s.log.Info("machine left", "org", c.org, "machine", c.machine)
	writeJSON(w, http.StatusOK, map[string]bool{"left": true})
}

// handleConnect holds a daemon's connection: it takes agent reports and
// carries relayed calls and their results.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request, c caller) {
	if !machinesOnly(w, c) {
		return
	}
	// The HTTP server's read and write timeouts must not end a long-lived connection.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(64 << 20) // a read result carries a transcript
	k := key{c.org, c.machine}
	s.mu.Lock()
	m := s.machines[k]
	if m == nil {
		m = &machine{}
		s.machines[k] = m
	}
	if m.conn != nil {
		m.conn.Close(websocket.StatusPolicyViolation, "replaced by a newer connection")
		for id, ch := range m.pending {
			ch <- pool.Frame{Type: "result", ID: id, Error: pool.Errorf("offline", "%s reconnected", c.machine)}
		}
	}
	m.conn, m.pending = conn, map[string]chan pool.Frame{}
	s.mu.Unlock()
	s.log.Info("machine connected", "org", c.org, "machine", c.machine)

	ctx := r.Context()
	go s.keepAlive(ctx, conn)
	for {
		var f pool.Frame
		if err := wsjson.Read(ctx, conn, &f); err != nil {
			break
		}
		switch f.Type {
		case "agents":
			s.report(k, f.Agents)
		case "result":
			s.mu.Lock()
			ch := m.pending[f.ID]
			delete(m.pending, f.ID)
			s.mu.Unlock()
			if ch != nil {
				ch <- f
			}
		}
	}
	s.mu.Lock()
	if m.conn == conn {
		m.conn = nil
		for id, ch := range m.pending {
			ch <- pool.Frame{Type: "result", ID: id, Error: pool.Errorf("offline", "%s disconnected", c.machine)}
		}
		m.pending = nil
	}
	s.mu.Unlock()
	conn.CloseNow()
	s.log.Info("machine disconnected", "org", c.org, "machine", c.machine)
}

func (s *Server) keepAlive(ctx context.Context, conn *websocket.Conn) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := conn.Ping(pctx)
		cancel()
		if err != nil {
			conn.CloseNow()
			return
		}
	}
}

// report replaces a machine's agents, forcing IDs into its namespace so a
// machine can only speak for its own agents.
func (s *Server) report(k key, list []pool.Agent) {
	kept := make([]pool.Agent, 0, len(list))
	for _, a := range list {
		if machine, _, ok := pool.SplitID(a.ID); !ok || machine != k.name {
			continue
		}
		a.Machine = k.name
		kept = append(kept, a)
	}
	s.mu.Lock()
	m := s.machines[k]
	if m != nil {
		m.agents, m.seen = kept, time.Now()
	}
	s.mu.Unlock()
}

// agents returns every agent in the org; a disconnected machine's agents are offline.
func (s *Server) agents(org string) []pool.Agent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []pool.Agent
	for k, m := range s.machines {
		if k.org != org {
			continue
		}
		for _, a := range m.agents {
			if m.conn == nil {
				a.Status = pool.StatusOffline
			}
			out = append(out, a)
		}
	}
	return byLastActive(out)
}

// byLastActive orders agents most recently active first, each main agent
// followed by the agents running inside it.
func byLastActive(list []pool.Agent) []pool.Agent {
	present := map[string]bool{}
	for _, a := range list {
		present[a.ID] = true
	}
	var roots []pool.Agent
	children := map[string][]pool.Agent{}
	for _, a := range list {
		if a.Root != "" && present[a.Root] {
			children[a.Root] = append(children[a.Root], a)
		} else {
			roots = append(roots, a)
		}
	}
	latest := func(a pool.Agent) string {
		t := a.LastActive
		for _, c := range children[a.ID] {
			t = max(t, c.LastActive)
		}
		return t
	}
	newest := func(xs []pool.Agent, at func(pool.Agent) string) {
		sort.SliceStable(xs, func(i, j int) bool {
			if at(xs[i]) != at(xs[j]) {
				return at(xs[i]) > at(xs[j])
			}
			return xs[i].ID < xs[j].ID
		})
	}
	newest(roots, latest)
	out := make([]pool.Agent, 0, len(list))
	for _, r := range roots {
		out = append(out, r)
		kids := children[r.ID]
		newest(kids, func(a pool.Agent) string { return a.LastActive })
		out = append(out, kids...)
	}
	return out
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request, c caller) {
	if !allowed(w, c, "list") {
		return
	}
	var sel pool.Selector
	if !decode(w, r, &sel) {
		return
	}
	out := []pool.Agent{}
	for _, a := range s.agents(c.org) {
		if sel.Matches(a) {
			out = append(out, a)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// call relays one call to a machine and waits for its result.
func (s *Server) call(ctx context.Context, org, name, method string, input any) (json.RawMessage, *pool.Error) {
	timeout := s.CallTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return s.callFor(ctx, timeout, org, name, method, input)
}

// callFor is call with its own time limit.
func (s *Server) callFor(ctx context.Context, timeout time.Duration, org, name, method string, input any) (json.RawMessage, *pool.Error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, pool.AsError(err)
	}
	id := strconv.FormatUint(s.seq.Add(1), 36)
	ch := make(chan pool.Frame, 1)
	s.mu.Lock()
	m := s.machines[key{org, name}]
	if m == nil || m.conn == nil {
		s.mu.Unlock()
		return nil, pool.Errorf("offline", "machine %s is not connected", name)
	}
	conn := m.conn
	m.pending[id] = ch
	s.mu.Unlock()
	if err := wsjson.Write(ctx, conn, pool.Frame{Type: "call", ID: id, Name: method, Input: raw}); err != nil {
		s.forget(org, name, id)
		return nil, pool.Errorf("offline", "machine %s: %v", name, err)
	}
	select {
	case f := <-ch:
		return f.Output, f.Error
	case <-ctx.Done():
		s.forget(org, name, id)
		return nil, pool.Errorf("timeout", "machine %s did not answer within %s", name, timeout)
	}
}

func (s *Server) forget(org, name, id string) {
	s.mu.Lock()
	if m := s.machines[key{org, name}]; m != nil && m.pending != nil {
		delete(m.pending, id)
	}
	s.mu.Unlock()
}

func statusFor(e *pool.Error) int {
	switch e.Code {
	case "not_found":
		return http.StatusNotFound
	case "forbidden":
		return http.StatusForbidden
	case "offline":
		return http.StatusServiceUnavailable
	case "timeout":
		return http.StatusGatewayTimeout
	case "unsupported", "bad_request":
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request, c caller) {
	if !allowed(w, c, "read") {
		return
	}
	var in struct {
		ID string `json:"id"`
		pool.ReadOptions
	}
	if !decode(w, r, &in) {
		return
	}
	machine, session, ok := pool.SplitID(in.ID)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("%q is not an agent ID like machine/cc-1234abcd", in.ID))
		return
	}
	out, perr := s.call(r.Context(), c.org, machine, "read", pool.LocalRead{ID: session, ReadOptions: in.ReadOptions})
	if perr != nil {
		writeError(w, statusFor(perr), perr.Code, perr.Message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// handleAsk answers a question about one agent from its transcript, on the machine that runs it. It reads the
// agent's context and changes nothing, so it needs the same capability as read.
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request, c caller) {
	if !allowed(w, c, "read") {
		return
	}
	var in pool.AskRequest
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Question) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "question is empty")
		return
	}
	machine, session, ok := pool.SplitID(in.ID)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("%q is not an agent ID like machine/cc-1234abcd", in.ID))
		return
	}
	from := sender(in.From, c.machine)
	if c.mod != nil {
		from, in.Agent = "mod "+c.mod.Mod, ""
	}
	replyTo := ""
	if in.Agent != "" {
		if _, _, ok := pool.SplitID(c.machine + "/" + in.Agent); ok {
			replyTo = c.machine + "/" + in.Agent
		}
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(pool.AskWait + time.Minute))
	out, perr := s.callFor(r.Context(), pool.AskWait, c.org, machine, "ask", pool.LocalAsk{ID: session, Question: in.Question, From: from, ReplyTo: replyTo})
	if perr != nil {
		writeError(w, statusFor(perr), perr.Code, perr.Message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// sender is the name a message is delivered under. The machine comes from the
// caller's credential, so no machine can speak as another; a name the caller
// gives is only a label in front of it.
func sender(label, machine string) string {
	label = strings.Join(strings.Fields(label), " ")
	if label == "" || label == machine {
		return machine
	}
	if r := []rune(label); len(r) > 64 {
		label = string(r[:64])
	}
	return label + "@" + machine
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request, c caller) {
	var in pool.SendRequest
	if !decode(w, r, &in) {
		return
	}
	// Posting to people needs only the slack capability; messaging agents needs send.
	if in.Slack != "" {
		if !allowed(w, c, "slack") {
			return
		}
	} else if !allowed(w, c, "send") {
		return
	}
	if c.mod != nil { // a mod speaks as itself, and no agent replies to it
		in.Agent = ""
	}
	if strings.TrimSpace(in.Text) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "message is empty")
		return
	}
	if in.Slack != "" {
		if !in.Target.IsZero() {
			writeError(w, http.StatusBadRequest, "bad_request", "send to agents or to Slack, not both at once")
			return
		}
		from := sender(in.From, c.machine)
		if c.mod != nil {
			from = "mod " + c.mod.Mod
		}
		replyTo := ""
		if in.Agent != "" {
			if _, _, ok := pool.SplitID(c.machine + "/" + in.Agent); ok {
				replyTo = c.machine + "/" + in.Agent
			}
		}
		s.slackSend(w, r, c, in, from, replyTo)
		return
	}
	if in.Target.IsZero() {
		writeError(w, http.StatusBadRequest, "bad_request", "name a target: an agent ID, or all, machine, cwd, or kind")
		return
	}
	from := sender(in.From, c.machine)
	if c.mod != nil {
		from = "mod " + c.mod.Mod
	}
	replyTo := ""
	if in.Agent != "" {
		if _, _, ok := pool.SplitID(c.machine + "/" + in.Agent); ok {
			replyTo = c.machine + "/" + in.Agent
		}
	}
	var targets []string
	if in.Target.ID != "" {
		if _, _, ok := pool.SplitID(in.Target.ID); !ok {
			writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("%q is not an agent ID like machine/cc-1234abcd", in.Target.ID))
			return
		}
		targets = []string{in.Target.ID}
	} else {
		// A group goes to main agents only; their subagents hear it through them.
		for _, a := range s.agents(c.org) {
			// The sending agent is left out of its own group.
			if a.Role == pool.RoleMain && a.Status != pool.StatusOffline && a.ID != replyTo && in.Target.Matches(a) {
				targets = append(targets, a.ID)
			}
		}
		if len(targets) == 0 {
			writeError(w, http.StatusNotFound, "not_found", "no connected agent matches")
			return
		}
	}
	results := make([]pool.SendResult, len(targets))
	var wg sync.WaitGroup
	for i, id := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			machine, session, _ := pool.SplitID(id)
			raw, perr := s.call(r.Context(), c.org, machine, "send", pool.LocalSend{ID: session, Text: in.Text, From: from, ReplyTo: replyTo})
			res := pool.SendResult{ID: id, Error: perr}
			if perr == nil {
				if err := json.Unmarshal(raw, &res); err != nil {
					res = pool.SendResult{ID: id, Error: pool.AsError(err)}
				}
			}
			results[i] = res
		}()
	}
	wg.Wait()
	if len(results) == 1 && results[0].Error != nil && in.Target.ID != "" {
		writeError(w, statusFor(results[0].Error), results[0].Error.Code, results[0].Error.Message)
		return
	}
	writeJSON(w, http.StatusOK, pool.SendResponse{Results: results})
}

func (s *Server) modsOn(w http.ResponseWriter) bool {
	if s.Mods == nil {
		writeError(w, http.StatusNotImplemented, "unsupported", "this server does not run mods")
		return false
	}
	return true
}

func modError(w http.ResponseWriter, err error) {
	var e *mods.Error
	if errors.As(err, &e) {
		status := map[string]int{"not_found": http.StatusNotFound, "bad_request": http.StatusBadRequest,
			"mod_failed": http.StatusUnprocessableEntity}[e.Code]
		if status == 0 {
			status = http.StatusBadRequest
		}
		writeError(w, status, e.Code, e.Message)
		return
	}
	writeError(w, http.StatusInternalServerError, "failed", err.Error())
}

func (s *Server) handleMods(w http.ResponseWriter, r *http.Request, c caller) {
	if !s.modsOn(w) || !machinesOnly(w, c) {
		return
	}
	list, err := s.Mods.List(c.org)
	if err != nil {
		modError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request, c caller) {
	if !s.modsOn(w) || !machinesOnly(w, c) {
		return
	}
	var in struct {
		Source string `json:"source"`
		Agent  string `json:"agent"`
	}
	if !decode(w, r, &in) {
		return
	}
	author := c.machine
	if in.Agent != "" {
		author = c.machine + "/" + in.Agent
	}
	res, err := s.Mods.Deploy(r.Context(), c.org, author, in.Source)
	if err != nil {
		modError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleCall runs a mod for a machine (the CLI, an agent) or for another mod with "call".
func (s *Server) handleCall(w http.ResponseWriter, r *http.Request, c caller) {
	if !s.modsOn(w) || !allowed(w, c, "call") {
		return
	}
	var in struct {
		Input json.RawMessage `json:"input"`
		Agent string          `json:"agent"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.allowLongWrite(w)
	who, depth := mods.Caller{Kind: "machine", Machine: c.machine}, 0
	switch {
	case c.mod != nil:
		who, depth = mods.Caller{Kind: "mod", Mod: c.mod.Mod}, c.mod.Depth+1
	case in.Agent != "":
		who = mods.Caller{Kind: "agent", Machine: c.machine, Agent: c.machine + "/" + in.Agent}
	}
	out, err := s.Mods.Call(r.Context(), c.org, r.PathValue("name"), in.Input, who, depth)
	if err != nil {
		modError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]json.RawMessage{"output": out})
}

// allowLongWrite lets a response wait for a mod to finish: the HTTP server's
// write timeout is far shorter than a mod's time limit.
func (s *Server) allowLongWrite(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(s.Mods.Timeout + time.Minute))
}

// handleKV is a running mod's own key-value store.
func (s *Server) handleKV(w http.ResponseWriter, r *http.Request, c caller) {
	if !s.modsOn(w) {
		return
	}
	if c.mod == nil {
		writeError(w, http.StatusForbidden, "forbidden", "only a running mod has a key-value store")
		return
	}
	if !allowed(w, c, "kv") {
		return
	}
	var in struct {
		Op       string          `json:"op"`
		Key      string          `json:"key"`
		Value    json.RawMessage `json:"value"`
		Expected json.RawMessage `json:"expected"`
	}
	if !decode(w, r, &in) {
		return
	}
	value, written, err := s.Mods.KV(*c.mod, in.Op, in.Key, in.Value, in.Expected)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if value == nil {
		value = json.RawMessage("null")
	}
	writeJSON(w, http.StatusOK, map[string]any{"value": value, "written": written})
}
