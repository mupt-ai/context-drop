package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// The setup screens follow dari's onboarding: each question takes over the
// alternate screen, and once it is answered the terminal returns to its normal
// screen and leaves a one-line checklist entry behind.

const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiCoral = "\x1b[38;5;209m"
	ansiCyan  = "\x1b[38;5;117m"
	ansiGreen = "\x1b[38;5;114m"
	ansiGray  = "\x1b[38;5;245m"
	ansiRed   = "\x1b[38;5;203m"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// termUI is the setup interface at a terminal. It puts the terminal in raw mode
// for the whole setup and restores it in close.
type termUI struct {
	in      *os.File
	out     io.Writer
	restore func()
}

func newTermUI(cmd *cobra.Command) *termUI {
	in, _ := cmd.InOrStdin().(*os.File)
	return &termUI{in: in, out: cmd.OutOrStdout()}
}

// keyPress is one decoded key: a name ("up", "down", "enter", "esc",
// "backspace", "ctrl-c", "left", "right") or typed text.
type keyPress struct {
	name string
	text string
}

// parseKeys decodes the bytes read from the terminal in one call.
func parseKeys(b []byte) []keyPress {
	var out []keyPress
	var text []byte
	flush := func() {
		if len(text) > 0 {
			out = append(out, keyPress{name: "text", text: string(text)})
			text = nil
		}
	}
	name := func(n string) {
		flush()
		out = append(out, keyPress{name: n})
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c == 27 && i+2 < len(b) && b[i+1] == '[':
			switch b[i+2] {
			case 'A':
				name("up")
			case 'B':
				name("down")
			case 'C':
				name("right")
			case 'D':
				name("left")
			}
			i += 2
		case c == 27:
			name("esc")
		case c == 3:
			name("ctrl-c")
		case c == '\r' || c == '\n':
			name("enter")
		case c == 127 || c == 8:
			name("backspace")
		case c >= 32:
			text = append(text, c)
		}
	}
	flush()
	return out
}

func (t *termUI) begin() error {
	if t.restore != nil {
		return nil
	}
	if t.in == nil {
		return errors.New("setup needs a terminal")
	}
	fd := int(t.in.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	t.restore = func() { _ = term.Restore(fd, state) }
	return nil
}

func (t *termUI) close() {
	if t.restore != nil {
		t.restore()
		t.restore = nil
	}
}

// write prints s, translating newlines for raw mode.
func (t *termUI) write(s string) {
	fmt.Fprint(t.out, strings.ReplaceAll(s, "\n", "\r\n"))
}

func (t *termUI) enterScreen() { t.write("\x1b[?1049h\x1b[?25l") }
func (t *termUI) leaveScreen() { t.write("\x1b[?25h\x1b[?1049l") }

func (t *termUI) draw(lines []string) {
	t.write("\x1b[H\x1b[2J" + strings.Join(lines, "\n") + "\n")
}

// pageLines lays out one question: its title, description, body, and key hints.
func pageLines(title, desc string, body []string, hint string) []string {
	lines := []string{"", "  " + ansiBold + ansiCoral + "Context Drop setup" + ansiReset, "", "  " + ansiBold + title + ansiReset}
	if desc != "" {
		lines = append(lines, "  "+ansiGray+desc+ansiReset)
	}
	lines = append(lines, "")
	for _, b := range body {
		lines = append(lines, "  "+b)
	}
	if hint != "" {
		lines = append(lines, "", "  "+ansiGray+hint+ansiReset)
	}
	return lines
}

// interact shows a page and feeds keys to handle until it reports done.
// Reading the terminal ending, or a cancel key, returns errAborted.
func (t *termUI) interact(render func() []string, handle func(keyPress) (bool, error)) error {
	if err := t.begin(); err != nil {
		return err
	}
	t.enterScreen()
	defer t.leaveScreen()
	buf := make([]byte, 64)
	for {
		t.draw(render())
		n, err := t.in.Read(buf)
		if err != nil {
			return errAborted
		}
		for _, k := range parseKeys(buf[:n]) {
			done, err := handle(k)
			if err != nil || done {
				return err
			}
		}
	}
}

// summary leaves one checklist line on the normal screen.
func (t *termUI) summary(title, value string) {
	t.write("  " + ansiGreen + "✓" + ansiReset + " " + title + "  " + ansiGray + value + ansiReset + "\n")
}

func (t *termUI) choose(title, desc string, options []string, def int) (int, error) {
	cur := def
	err := t.interact(func() []string {
		body := make([]string, len(options))
		for i, o := range options {
			if i == cur {
				body[i] = ansiCyan + "› " + o + ansiReset
			} else {
				body[i] = "  " + o
			}
		}
		return pageLines(title, desc, body, "↑↓ move · enter choose · esc cancel")
	}, func(k keyPress) (bool, error) {
		switch {
		case k.name == "up" || k.text == "k":
			if cur > 0 {
				cur--
			}
		case k.name == "down" || k.text == "j":
			if cur < len(options)-1 {
				cur++
			}
		case k.name == "enter":
			return true, nil
		case k.name == "esc" || k.name == "ctrl-c":
			return true, errAborted
		}
		return false, nil
	})
	if err != nil {
		return cur, err
	}
	t.summary(title, options[cur])
	return cur, nil
}

func (t *termUI) ask(title, desc, def string, validate func(string) error) (string, error) {
	return t.input(title, desc, def, validate, false)
}

// secret asks for a value shown as dots. The value is never saved by setup
// except where the caller stores it.
func (t *termUI) secret(title, desc string, validate func(string) error) (string, error) {
	return t.input(title, desc, "", validate, true)
}

func (t *termUI) input(title, desc, def string, validate func(string) error, masked bool) (string, error) {
	value := []rune(def)
	var result, problem string
	err := t.interact(func() []string {
		shown := string(value)
		if masked {
			shown = strings.Repeat("•", len(value))
		}
		body := []string{ansiCyan + "› " + ansiReset + shown + ansiCyan + "▏" + ansiReset}
		if problem != "" {
			body = append(body, ansiRed+problem+ansiReset)
		}
		return pageLines(title, desc, body, "enter confirm · esc cancel")
	}, func(k keyPress) (bool, error) {
		switch {
		case k.name == "esc" || k.name == "ctrl-c":
			return true, errAborted
		case k.name == "backspace":
			if len(value) > 0 {
				value = value[:len(value)-1]
			}
		case k.name == "text":
			value = append(value, []rune(k.text)...)
		case k.name == "enter":
			v := strings.TrimSpace(string(value))
			if v == "" {
				v = def
			}
			if validate != nil {
				if err := validate(v); err != nil {
					problem = err.Error()
					return false, nil
				}
			}
			result = v
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return "", err
	}
	shown := result
	if masked && result != "" {
		shown = strings.Repeat("•", len([]rune(result)))
	}
	t.summary(title, shown)
	return result, nil
}

func (t *termUI) confirm(title, desc string, def bool) (bool, error) {
	options := []string{"Yes", "No"}
	cur := 1
	if def {
		cur = 0
	}
	err := t.interact(func() []string {
		body := make([]string, len(options))
		for i, o := range options {
			if i == cur {
				body[i] = ansiCyan + "› " + o + ansiReset
			} else {
				body[i] = "  " + o
			}
		}
		return pageLines(title, desc, body, "←→ move · y/n · enter confirm · esc cancel")
	}, func(k keyPress) (bool, error) {
		switch {
		case k.name == "left" || k.name == "up":
			cur = 0
		case k.name == "right" || k.name == "down":
			cur = 1
		case k.text == "y" || k.text == "Y":
			cur = 0
			return true, nil
		case k.text == "n" || k.text == "N":
			cur = 1
			return true, nil
		case k.name == "enter":
			return true, nil
		case k.name == "esc" || k.name == "ctrl-c":
			return true, errAborted
		}
		return false, nil
	})
	if err != nil {
		return false, err
	}
	t.summary(title, options[cur])
	return cur == 0, nil
}

func (t *termUI) note(text string) { t.write(text + "\n") }

// run shows a spinner on its own page while work runs, then leaves a checklist line.
func (t *termUI) run(title string, work func() error) error {
	if err := t.begin(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- work() }()
	t.enterScreen()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	var err error
	for i := 0; ; i++ {
		select {
		case err = <-done:
			t.leaveScreen()
			if err != nil {
				t.write("  " + ansiRed + "✗" + ansiReset + " " + title + "\n")
			} else {
				t.summary(title, "done")
			}
			return err
		case <-tick.C:
			t.draw(pageLines(title, "", []string{ansiCyan + spinnerFrames[i%len(spinnerFrames)] + ansiReset + " working…"}, ""))
		}
	}
}
