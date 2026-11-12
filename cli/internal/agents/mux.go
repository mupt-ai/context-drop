package agents

import (
	"context"
	"fmt"
	"strings"
)

// Multiplexers Context Drop can start agents in, locate them in, and type into.
const (
	MuxTmux  = "tmux"
	MuxHerdr = "herdr"
	MuxRex   = "rex"
)

// Multiplexers lists the supported multiplexers, in the order a process is
// looked up in them (innermost first: a tmux running inside a rex block wins).
var Multiplexers = []string{MuxTmux, MuxHerdr, MuxRex}

// Placement says where to open a new pane, in each multiplexer's own terms:
//
//	        Session    Window  Pane
//	tmux    session    window  pane
//	herdr   workspace  tab     pane
//	rex     session    window  block
//
// A Window or Pane opens a new pane split beside it; a Session alone opens a
// new window (a tab in herdr). Each value is an ID or a label. Empty fields
// leave the choice to the multiplexer.
type Placement struct {
	Session string `json:"session,omitempty"`
	Window  string `json:"window,omitempty"`
	Pane    string `json:"pane,omitempty"`
}

// IsZero reports whether no place is named.
func (p Placement) IsZero() bool { return p.Session == "" && p.Window == "" && p.Pane == "" }

// Location is the pane a process runs in.
type Location struct {
	Mux     string `json:"mux"`
	Session string `json:"session,omitempty"`
	Window  string `json:"window,omitempty"`
	Pane    string `json:"pane"`
	Label   string `json:"label,omitempty"`

	// scope qualifies Pane in commands (the rex session ID).
	scope string
	// aliases are the other names each level answers to: [session, window, pane].
	aliases [3][]string
}

// describe names the pane as a delivery channel, e.g. "rex pane block:1".
func (l Location) describe() string {
	if l.Mux == "" {
		return ""
	}
	return l.Mux + " pane " + l.Pane
}

// String is a short human-readable place, e.g. "rex calm-prairie/reviewer".
func (l Location) String() string {
	switch l.Mux {
	case "":
		return ""
	case MuxTmux:
		return fmt.Sprintf("tmux %s:%s %s", l.Session, l.Window, l.Pane)
	case MuxRex:
		return "rex " + strings.Trim(l.Session+"/"+firstNonEmpty(l.Label, shortRexID(l.Pane)), "/")
	}
	return l.Mux + " " + l.Pane
}

// Matches reports whether the location is in the named place. Each named
// level must match the location's ID or label at that level.
func (l Location) Matches(at Placement) bool {
	want := [3]string{at.Session, at.Window, at.Pane}
	have := [3][]string{{l.Session}, {l.Window}, {l.Pane, l.Label}}
	for i, w := range want {
		if w != "" && !nameMatches(w, append(have[i], l.aliases[i]...)...) {
			return false
		}
	}
	return true
}

// nameMatches compares a user-supplied name with a pane's names. Rex IDs
// ("block:01a1…") also match without their kind and by the short form Rex
// prints (a prefix or suffix of at least six characters).
func nameMatches(want string, names ...string) bool {
	for _, n := range names {
		if n == "" {
			continue
		}
		if strings.EqualFold(n, want) {
			return true
		}
		// Only rex IDs have a word kind; herdr's "w1:p2" does not.
		if kind, id, ok := strings.Cut(n, ":"); ok && len(want) >= 6 && (kind == "session" || kind == "window" || kind == "block") &&
			(strings.HasPrefix(id, strings.TrimPrefix(want, kind+":")) || strings.HasSuffix(id, want)) {
			return true
		}
	}
	return false
}

func shortRexID(id string) string {
	if len(id) > 8 && strings.Contains(id, ":") {
		return id[len(id)-8:]
	}
	return id
}

// multiplexer is what Context Drop needs from tmux, herdr, and rex.
type multiplexer interface {
	// open starts argv in a new pane at the placement.
	open(ctx context.Context, env *Env, at Placement, dir, label string, argv []string) (Location, error)
	// locate finds the pane that runs pid (or owns tty).
	locate(ctx context.Context, env *Env, pid int, tty string) (Location, bool)
	// capture returns about the last lines of the pane's screen.
	capture(ctx context.Context, env *Env, at Location, lines int) (string, error)
	// submit types text into the pane as one line and presses Enter.
	submit(ctx context.Context, env *Env, at Location, text string) error
	// press presses a key: "enter" or "down".
	press(ctx context.Context, env *Env, at Location, key string) error
}

var muxes = map[string]multiplexer{MuxTmux: tmuxMux{}, MuxHerdr: herdrMux{}, MuxRex: rexMux{}}

func muxFor(name string) (multiplexer, error) {
	if m, ok := muxes[name]; ok {
		return m, nil
	}
	return nil, fmt.Errorf("unknown multiplexer %q (want tmux, herdr, or rex)", name)
}

// locate finds the pane that runs pid in any multiplexer.
func locate(ctx context.Context, env *Env, pid int, tty string) (Location, bool) {
	for _, name := range Multiplexers {
		if loc, ok := muxes[name].locate(ctx, env, pid, tty); ok {
			return loc, true
		}
	}
	return Location{}, false
}

// Locate fills in the Location of every session whose process runs in a
// tmux, herdr, or rex pane, whether or not Context Drop started it.
func Locate(ctx context.Context, env *Env, sessions []Session) {
	found := map[int]*Location{}
	for i := range sessions {
		s := &sessions[i]
		if s.Location != nil || s.PID <= 0 {
			continue
		}
		loc, seen := found[s.PID]
		if !seen {
			if l, ok := locate(ctx, env, s.PID, s.TTY); ok {
				loc = &l
			}
			found[s.PID] = loc
		}
		if loc != nil {
			l := *loc
			s.Location = &l
		}
	}
}

// Capture returns the last lines of the screen of the pane s runs in.
func Capture(ctx context.Context, env *Env, s Session, lines int) (string, error) {
	if s.Location == nil && s.PID > 0 {
		if loc, ok := locate(ctx, env, s.PID, s.TTY); ok {
			s.Location = &loc
		}
	}
	if s.Location == nil {
		return "", fmt.Errorf("%s is not running in a tmux, herdr, or rex pane", s.ID)
	}
	m, err := muxFor(s.Location.Mux)
	if err != nil {
		return "", err
	}
	return m.capture(ctx, env, *s.Location, lines)
}

// ancestorPane walks pid and its parents until one is a process known to a
// pane, so a wrapper such as `dari pi` still matches.
func ancestorPane(ctx context.Context, env *Env, pid int, panes map[int]Location) (Location, bool) {
	if len(panes) == 0 {
		return Location{}, false
	}
	procs, _ := env.Procs(ctx)
	for seen := 0; pid > 1 && seen < 64; seen++ {
		if loc, ok := panes[pid]; ok {
			return loc, true
		}
		p, ok := procs[pid]
		if !ok {
			break
		}
		pid = p.PPID
	}
	return Location{}, false
}

// singleLine joins a message into one line: a newline would submit early in
// most terminal UIs.
func singleLine(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, "\r\n", "\n")), " ")
}

// lastLines keeps the last n lines of s, without trailing blank lines.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, " \n"), "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
