package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"contextdrop.dev/context-drop/internal/agents"
	"contextdrop.dev/context-drop/internal/config"
	"contextdrop.dev/context-drop/internal/summarize"
	"github.com/spf13/cobra"
)

// Seams for tests.
var (
	isInteractive = func(cmd *cobra.Command) bool {
		return isTerminal(cmd.InOrStdin()) && isTerminal(cmd.OutOrStdout())
	}
	lookPath  = exec.LookPath
	getenv    = os.Getenv
	runOutput = func(name string, args ...string) ([]byte, error) { return exec.Command(name, args...).Output() }
	testModel = func(ctx context.Context, sc config.SummarizeConfig) error {
		m, err := summarize.NewModel(sc, os.Getenv, nil)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		_, err = m.Complete(ctx, "Answer with one word.", "Reply with OK.")
		return err
	}
)

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// setupUI is how the wizard asks questions: a TUI at a terminal, a scripted
// prompter in tests. Every method returns errAborted when the user cancels.
type setupUI interface {
	choose(title, desc string, options []string, def int) (int, error)
	ask(title, desc, def string, validate func(string) error) (string, error)
	secret(title, desc string, validate func(string) error) (string, error)
	confirm(title, desc string, def bool) (bool, error)
	note(text string)
	run(title string, work func() error) error
}

var errAborted = errors.New("setup cancelled; nothing was saved")

// tui renders each question as a huh form.
type tui struct {
	in  io.Reader
	out io.Writer
}

func newTUI(cmd *cobra.Command) *tui { return &tui{in: cmd.InOrStdin(), out: cmd.OutOrStdout()} }

func (t *tui) form(f huh.Field) error {
	err := huh.NewForm(huh.NewGroup(f)).WithTheme(huh.ThemeCharm()).WithInput(t.in).WithOutput(t.out).Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return errAborted
	}
	return err
}

func (t *tui) choose(title, desc string, options []string, def int) (int, error) {
	opts := make([]huh.Option[int], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o, i)
	}
	v := def
	err := t.form(huh.NewSelect[int]().Title(title).Description(desc).Options(opts...).Value(&v))
	return v, err
}

func (t *tui) ask(title, desc, def string, validate func(string) error) (string, error) {
	v := def
	in := huh.NewInput().Title(title).Description(desc).Value(&v)
	if validate != nil {
		in = in.Validate(validate)
	}
	err := t.form(in)
	return strings.TrimSpace(v), err
}

// secret asks for a value without echoing it.
func (t *tui) secret(title, desc string, validate func(string) error) (string, error) {
	v := ""
	in := huh.NewInput().Title(title).Description(desc).EchoMode(huh.EchoModePassword).Value(&v)
	if validate != nil {
		in = in.Validate(validate)
	}
	err := t.form(in)
	return strings.TrimSpace(v), err
}

func (t *tui) confirm(title, desc string, def bool) (bool, error) {
	v := def
	err := t.form(huh.NewConfirm().Title(title).Description(desc).Affirmative("Yes").Negative("No").Value(&v))
	return v, err
}

func (t *tui) note(text string) { fmt.Fprintln(t.out, noteStyle.Render(text)) }

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")).MarginTop(1)
	noteStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

// run shows a spinner while work runs.
func (t *tui) run(title string, work func() error) error {
	done := make(chan error, 1)
	go func() { done <- work() }()
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for i := 0; ; i++ {
		select {
		case err := <-done:
			if err != nil {
				fmt.Fprintf(t.out, "\r\033[K%s %s\n", errStyle.Render("✗"), title)
			} else {
				fmt.Fprintf(t.out, "\r\033[K%s %s\n", okStyle.Render("✓"), title)
			}
			return err
		case <-tick.C:
			fmt.Fprintf(t.out, "\r\033[K%s %s", frames[i%len(frames)], title)
		}
	}
}

// prompter asks plain line-based questions; tests script it.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func newPrompter(cmd *cobra.Command) *prompter {
	return &prompter{in: bufio.NewReader(cmd.InOrStdin()), out: cmd.OutOrStdout()}
}

func (p *prompter) line() (string, error) {
	s, err := p.in.ReadString('\n')
	if err != nil && s == "" {
		return "", errAborted
	}
	return strings.TrimSpace(s), nil
}

func (p *prompter) ask(title, desc, def string, validate func(string) error) (string, error) {
	if desc != "" {
		fmt.Fprintln(p.out, desc)
	}
	for {
		fmt.Fprintf(p.out, "%s [%s]: ", title, def)
		a, err := p.line()
		if err != nil {
			return "", err
		}
		if a == "" {
			a = def
		}
		if validate != nil {
			if err := validate(a); err != nil {
				fmt.Fprintln(p.out, err)
				continue
			}
		}
		return a, nil
	}
}

func (p *prompter) secret(title, desc string, validate func(string) error) (string, error) {
	return p.ask(title, desc, "", validate)
}

func (p *prompter) confirm(title, desc string, def bool) (bool, error) {
	d := "n"
	if def {
		d = "y"
	}
	a, err := p.ask(title+" (y/n)", desc, d, nil)
	return strings.HasPrefix(strings.ToLower(a), "y"), err
}

func (p *prompter) choose(title, desc string, options []string, def int) (int, error) {
	fmt.Fprintln(p.out, title)
	for i, o := range options {
		fmt.Fprintf(p.out, "  %d) %s\n", i+1, o)
	}
	a, err := p.ask("Choose", desc, strconv.Itoa(def+1), func(a string) error {
		if n, err := strconv.Atoi(a); err != nil || n < 1 || n > len(options) {
			return fmt.Errorf("enter a number from 1 to %d", len(options))
		}
		return nil
	})
	n, _ := strconv.Atoi(a)
	return n - 1, err
}

func (p *prompter) note(text string) { fmt.Fprintln(p.out, text) }

func (p *prompter) run(title string, work func() error) error {
	err := work()
	if err != nil {
		fmt.Fprintf(p.out, "✗ %s\n", title)
	} else {
		fmt.Fprintf(p.out, "✓ %s\n", title)
	}
	return err
}

// runSetup walks through the summary model and the launcher and saves the
// result. Cancelling at any point saves nothing.
func runSetup(cmd *cobra.Command, ui setupUI) error {
	cfg, err := config.LoadCLIConfigFile()
	if err != nil {
		return err
	}
	ui.note(titleStyle.Render("Context Drop setup"))
	ui.note("Two parts: the model that summarizes your sessions, and how new agents are launched.")
	if ok, err := ui.confirm("Set up the summary model?", "Used by `context-drop summarize`.", true); err != nil {
		return err
	} else if ok {
		if cfg.Summarize, err = setupSummarize(cmd.Context(), ui, cfg.Summarize); err != nil {
			return err
		}
	}
	if ok, err := ui.confirm("Set up the agent launcher?", "Used by `context-drop start`.", true); err != nil {
		return err
	} else if ok {
		if cfg.LaunchIn, cfg.LaunchCommand, err = setupLauncher(ui, cfg.LaunchIn, cfg.LaunchCommand); err != nil {
			return err
		}
	}
	if ok, err := ui.confirm("Set up uploads?", "For `context-drop upload` links. Uses the main server by default.", true); err != nil {
		return err
	} else if ok {
		if err := setupUploads(ui, &cfg); err != nil {
			return err
		}
	}
	installPi := false
	var piPath string
	if _, err := lookPath("pi"); err == nil {
		if env, err := newEnv(); err == nil {
			piPath = env.PiExtensionPath()
			if _, statErr := os.Stat(piPath); statErr != nil {
				if installPi, err = ui.confirm("Install the Pi extension?", "Lets context-drop message Pi sessions in any terminal.", true); err != nil {
					return err
				}
			}
		}
	}
	cfg.SetupDone = true
	if err := config.SaveCLIConfig(cfg); err != nil {
		return err
	}
	path, _ := config.CLIConfigPath()
	ui.note(okStyle.Render("✓ Saved " + path))
	if installPi {
		if err := os.MkdirAll(filepath.Dir(piPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(piPath, agents.PiExtension, 0o644); err != nil {
			return err
		}
		ui.note(okStyle.Render("✓ Installed the Pi extension") + noteStyle.Render(" (restart running Pi sessions to load it)"))
	}
	return nil
}

// mainServer is the hosted Context Drop server uploads default to.
const mainServer = "https://contextdrop.dev"

// setupUploads sets the endpoint and upload token. The default endpoint is the
// main server, whose token you paste. For a server you run yourself, setup can
// generate a token and print the line that server needs.
func setupUploads(ui setupUI, cfg *config.CLIConfig) error {
	endpoint, err := ui.ask("Upload server", "The main server, or the URL of one you run.", firstNonEmptyString(cfg.Endpoint, mainServer), func(v string) error {
		u, err := url.Parse(strings.TrimSpace(v))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("enter a full URL such as https://contextdrop.dev")
		}
		return nil
	})
	if err != nil {
		return err
	}
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if err := ui.run("Checking "+endpoint, func() error { return checkEndpoint(endpoint) }); err != nil {
		ui.note(errStyle.Render(oneLineErr(err)) + noteStyle.Render(" (saved anyway; uploads will fail until it responds)"))
	}
	cfg.Endpoint = endpoint

	if endpoint != mainServer {
		generate, err := ui.confirm("Generate a token for this server?", "Choose No if you already have a token for it.", false)
		if err != nil {
			return err
		}
		if generate {
			token, err := newUploadToken()
			if err != nil {
				return err
			}
			cfg.UploadToken = token
			ui.note(okStyle.Render("✓ Generated a token. Start your server with this value:") + "\n  CONTEXT_DROP_UPLOAD_TOKEN=" + token)
			return nil
		}
	}
	token, err := ui.secret("Upload token", "Paste the token for this server. It is saved in ~/.context-drop/config.toml.", func(v string) error {
		if strings.TrimSpace(v) == "" {
			return errors.New("a token is required for uploads")
		}
		return nil
	})
	if err != nil {
		return err
	}
	cfg.UploadToken = token
	return nil
}

// checkEndpoint reports whether the server answers its health check.
var checkEndpoint = func(endpoint string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(endpoint + "/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s/health returned %s", endpoint, resp.Status)
	}
	return nil
}

// newUploadToken returns 256 random bits as hex.
var newUploadToken = func() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func setupSummarize(ctx context.Context, ui setupUI, cur config.SummarizeConfig) (config.SummarizeConfig, error) {
	labels := []string{
		"Ollama: a local model, transcripts stay on this machine",
		"Anthropic: Claude through the Anthropic API or a gateway",
		"OpenAI-compatible: OpenAI, LM Studio, llama.cpp, vLLM",
		"Command: any CLI that reads the prompt on stdin, like `claude -p`",
	}
	names := []string{summarize.ProviderOllama, summarize.ProviderAnthropic, summarize.ProviderOpenAI, summarize.ProviderCommand}
	def := indexOf(names, cur.Provider)
	for {
		i, err := ui.choose("Which model provider?", "Transcript excerpts are sent to it.", labels, def)
		if err != nil {
			return cur, err
		}
		sc := config.SummarizeConfig{Provider: names[i]}
		keep := func(v string) string {
			if cur.Provider == sc.Provider {
				return v
			}
			return ""
		}
		required := func(what string) func(string) error {
			return func(v string) error {
				if strings.TrimSpace(v) == "" {
					return fmt.Errorf("%s is required", what)
				}
				return nil
			}
		}
		switch sc.Provider {
		case summarize.ProviderOllama:
			models := ollamaModels()
			if len(models) > 0 {
				options := append(append([]string{}, models...), "another model…")
				j, err := ui.choose("Which Ollama model?", "Models installed in Ollama.", options, max(indexOf(models, keep(cur.Model)), 0))
				if err != nil {
					return cur, err
				}
				if j < len(models) {
					sc.Model = models[j]
				}
			}
			if sc.Model == "" {
				if sc.Model, err = ui.ask("Ollama model", "For example qwen3.8:27b-mlx.", keep(cur.Model), required("a model")); err != nil {
					return cur, err
				}
			}
			if sc.BaseURL, err = ui.ask("Ollama URL", "", firstNonEmptyString(keep(cur.BaseURL), ollamaHost(), "http://localhost:11434"), required("a URL")); err != nil {
				return cur, err
			}
		case summarize.ProviderAnthropic:
			if sc.Model, err = ui.ask("Model", "", firstNonEmptyString(keep(cur.Model), getenv("ANTHROPIC_DEFAULT_SONNET_MODEL"), "claude-sonnet-5-5"), required("a model")); err != nil {
				return cur, err
			}
			if sc.BaseURL, err = ui.ask("API base URL", "Change this to use a gateway.", firstNonEmptyString(keep(cur.BaseURL), getenv("ANTHROPIC_BASE_URL"), "https://api.anthropic.com"), required("a URL")); err != nil {
				return cur, err
			}
			if sc.BaseURL == "https://api.anthropic.com" {
				sc.BaseURL = ""
			}
			if sc.APIKeyEnv, err = askKeyEnv(ui, keep(cur.APIKeyEnv), true, "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"); err != nil {
				return cur, err
			}
		case summarize.ProviderOpenAI:
			if sc.BaseURL, err = ui.ask("API base URL", "OpenAI, or a local server such as http://localhost:1234/v1.", firstNonEmptyString(keep(cur.BaseURL), "https://api.openai.com/v1"), required("a URL")); err != nil {
				return cur, err
			}
			if sc.Model, err = ui.ask("Model", "", keep(cur.Model), required("a model")); err != nil {
				return cur, err
			}
			official := strings.TrimRight(sc.BaseURL, "/") == "https://api.openai.com/v1"
			if sc.APIKeyEnv, err = askKeyEnv(ui, keep(cur.APIKeyEnv), official, "OPENAI_API_KEY"); err != nil {
				return cur, err
			}
		case summarize.ProviderCommand:
			if sc.Command, err = ui.ask("Command", "Receives the prompt on stdin and prints the summary.", firstNonEmptyString(keep(cur.Command), "claude -p"), required("a command")); err != nil {
				return cur, err
			}
		}
		test, err := ui.confirm("Test it now?", "Sends a one-word request.", true)
		if err != nil || !test {
			return sc, err
		}
		testErr := ui.run("Testing "+sc.Provider+" "+firstNonEmptyString(sc.Model, sc.Command), func() error { return testModel(ctx, sc) })
		if testErr == nil {
			return sc, nil
		}
		ui.note(errStyle.Render(oneLineErr(testErr)))
		again, err := ui.confirm("Change the settings?", "Choose No to save them anyway.", true)
		if err != nil || !again {
			return sc, err
		}
		cur, def = sc, indexOf(names, sc.Provider)
	}
}

// askKeyEnv asks for the name of the environment variable that holds the key,
// suggesting ones that are set. Only the name is saved, never the key.
func askKeyEnv(ui setupUI, cur string, needed bool, candidates ...string) (string, error) {
	var set []string
	for _, c := range candidates {
		if getenv(c) != "" {
			set = append(set, c)
		}
	}
	def := cur
	if def == "" && len(set) > 0 {
		def = set[0]
	}
	desc := "The variable's name, not the key. Only the name is saved."
	if len(set) > 0 {
		desc += " Set in this shell: " + strings.Join(set, ", ") + "."
	}
	if !needed {
		desc += " Leave empty if the server needs no key."
	}
	name, err := ui.ask("API key variable", desc, def, func(v string) error {
		v = strings.TrimSpace(v)
		switch {
		case v == "" && needed:
			return errors.New("a variable name is required")
		case v != "" && !envName.MatchString(v):
			return errors.New("enter a variable name like ANTHROPIC_API_KEY, not the key itself")
		}
		return nil
	})
	if err == nil && name != "" && getenv(name) == "" {
		ui.note(noteStyle.Render("$" + name + " is not set in this shell; set it before running summarize."))
	}
	return name, err
}

var envName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func oneLineErr(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	if len(s) > 200 {
		s = s[:199] + "…"
	}
	return "✗ " + s
}

func setupLauncher(ui setupUI, curIn, curCmd string) (string, string, error) {
	options := []string{"Whichever I'm running in"}
	values := []string{""}
	for _, m := range agents.Multiplexers {
		if muxInstalled(m) {
			options = append(options, map[string]string{agents.MuxTmux: "tmux window", agents.MuxHerdr: "herdr tab", agents.MuxRex: "Rex block"}[m])
			values = append(values, m)
		}
	}
	i, err := ui.choose("Open new agents in", "", options, max(indexOf(values, curIn), 0))
	if err != nil {
		return curIn, curCmd, err
	}
	in := values[i]

	labels := []string{"Run the agent directly (claude, codex, pi, opencode)"}
	cmds := []string{""}
	if _, err := lookPath("dari"); err == nil {
		labels = append(labels, "Through dari (dari claude, dari codex, …)")
		cmds = append(cmds, "dari {agent}")
	}
	labels = append(labels, "A custom command")
	cmds = append(cmds, "custom")
	def := indexOf(cmds, curCmd)
	if curCmd != "" && cmds[def] != curCmd {
		def = len(cmds) - 1
	}
	j, err := ui.choose("Start agents by", "", labels, def)
	if err != nil {
		return curIn, curCmd, err
	}
	command := cmds[j]
	if command == "custom" {
		command, err = ui.ask("Command", "{agent} becomes claude, codex, pi, or opencode.", curCmd, func(v string) error {
			if strings.TrimSpace(v) == "" {
				return errors.New("a command is required")
			}
			return nil
		})
	}
	return in, command, err
}

func muxInstalled(m string) bool {
	if bin := getenv("CONTEXT_DROP_" + strings.ToUpper(m) + "_BIN"); bin != "" {
		_, err := lookPath(bin)
		return err == nil
	}
	switch m {
	case agents.MuxRex:
		if getenv("REX_BIN_DIR") != "" {
			return true
		}
		for _, p := range []string{"rex", "/Applications/Rex.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex", "/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex"} {
			if _, err := lookPath(p); err == nil {
				return true
			}
		}
		return false
	default:
		_, err := lookPath(m)
		return err == nil
	}
}

func ollamaModels() []string {
	out, err := runOutput("ollama", "list")
	if err != nil {
		return nil
	}
	var models []string
	for i, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); i > 0 && len(f) > 0 {
			models = append(models, f[0])
		}
	}
	return models
}

// indexOf returns v's position in list, or 0.
func indexOf(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return 0
}

// offerSetup asks whether to run setup when the user is at a terminal.
func offerSetup(cmd *cobra.Command, why string) (bool, error) {
	if !isInteractive(cmd) {
		return false, nil
	}
	ui := newTUI(cmd)
	ok, err := ui.confirm(why, "Set it up now?", true)
	if err != nil || !ok {
		return false, nil
	}
	return true, runSetup(cmd, ui)
}

// ollamaHost turns $OLLAMA_HOST (often host:port) into a URL.
func ollamaHost() string {
	h := getenv("OLLAMA_HOST")
	if h == "" || strings.Contains(h, "://") {
		return h
	}
	return "http://" + h
}
