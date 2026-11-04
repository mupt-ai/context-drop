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
	close()
}

var errAborted = errors.New("setup cancelled; nothing was saved")

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

func (p *prompter) close() {}

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
	defer ui.close()
	cfg, err := config.LoadCLIConfigFile()
	if err != nil {
		return err
	}
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
	ui.note(green("✓ Saved " + path))
	if installPi {
		if err := os.MkdirAll(filepath.Dir(piPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(piPath, agents.PiExtension, 0o644); err != nil {
			return err
		}
		ui.note(green("✓ Installed the Pi extension") + gray(" (restart running Pi sessions to load it)"))
	}
	return nil
}

// mainServer is the hosted Context Drop server uploads default to.
const mainServer = "https://contextdrop.dev"

// setupUploads uses the public main server without credentials. Private servers
// can optionally use a shared upload token.
func setupUploads(ui setupUI, cfg *config.CLIConfig) error {
	def := 0
	if cfg.Endpoint != "" && cfg.Endpoint != mainServer {
		def = 1
	}
	choice, err := ui.choose("Which upload server?", "", []string{"The main server (" + mainServer + ")", "A server I run"}, def)
	if err != nil {
		return err
	}
	endpoint := mainServer
	if choice == 1 {
		endpoint, err = ui.ask("Server URL", "For example https://files.example.com.", cfg.Endpoint, func(v string) error {
			u, err := url.Parse(strings.TrimSpace(v))
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return errors.New("enter a full URL such as https://files.example.com")
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if err := ui.run("Checking "+endpoint, func() error { return checkEndpoint(endpoint) }); err != nil {
		ui.note(red(oneLineErr(err)) + gray(" (saved anyway; uploads will fail until it responds)"))
	}
	cfg.Endpoint = endpoint
	if endpoint == mainServer {
		cfg.UploadToken = ""
		return nil
	}

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
			ui.note(green("✓ Generated a token. Start your server with this value:") + "\n  CONTEXT_DROP_UPLOAD_TOKEN=" + token)
			return nil
		}
	}
	token, err := ui.secret("Upload token (optional)", "Leave blank for a public server. Saved in ~/.context-drop/config.toml.", func(v string) error { return nil })
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

// modelChoice is one row of the summary model picker.
type modelChoice struct {
	label   string
	detail  string
	sc      config.SummarizeConfig
	keyEnvs []string // variables a cloud model's key may come from
	// ask is what to request after picking: "model", "url", or "command".
	ask string
}

var (
	anthropicKeyEnvs = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}
	openAIKeyEnvs    = []string{"OPENAI_API_KEY"}
)

// summaryChoices lists the cheap cloud models, the Ollama models installed on
// this machine, and the ways to enter a custom model.
func summaryChoices() []modelChoice {
	choices := []modelChoice{
		{label: "Claude Haiku 5.5", detail: "Anthropic · cheap and fast",
			sc: config.SummarizeConfig{Provider: summarize.ProviderAnthropic, Model: "claude-haiku-5-5"}, keyEnvs: anthropicKeyEnvs},
		{label: "GPT-6 Luna", detail: "OpenAI · cheap",
			sc: config.SummarizeConfig{Provider: summarize.ProviderOpenAI, Model: "gpt-6-luna", BaseURL: "https://api.openai.com/v1"}, keyEnvs: openAIKeyEnvs},
		{label: "Claude Sonnet 5.5", detail: "Anthropic · stronger, costs more",
			sc: config.SummarizeConfig{Provider: summarize.ProviderAnthropic, Model: "claude-sonnet-5-5"}, keyEnvs: anthropicKeyEnvs},
	}
	for _, m := range ollamaModels() {
		choices = append(choices, modelChoice{label: m, detail: "Ollama · installed here, runs locally",
			sc: config.SummarizeConfig{Provider: summarize.ProviderOllama, Model: m, BaseURL: ollamaBase()}})
	}
	return append(choices,
		modelChoice{label: "Other Ollama model…", detail: "type a model name", sc: config.SummarizeConfig{Provider: summarize.ProviderOllama}, ask: "model"},
		modelChoice{label: "OpenAI-compatible server…", detail: "LM Studio, llama.cpp, vLLM", sc: config.SummarizeConfig{Provider: summarize.ProviderOpenAI}, ask: "url"},
		modelChoice{label: "Command…", detail: "any CLI that reads the prompt on stdin", sc: config.SummarizeConfig{Provider: summarize.ProviderCommand}, ask: "command"},
	)
}

// keyStatusText says whether one of a cloud model's key variables is set in this shell.
func keyStatusText(envs []string) string {
	for _, e := range envs {
		if getenv(e) != "" {
			return "key in $" + e
		}
	}
	return "no key set"
}

// defaultChoice starts the picker on the model already configured, else on a
// cloud model whose key is set, else on the first installed Ollama model.
func defaultChoice(choices []modelChoice, cur config.SummarizeConfig) int {
	for i, c := range choices {
		if cur.Model != "" && c.sc.Model == cur.Model && c.sc.Provider == cur.Provider {
			return i
		}
	}
	for i, c := range choices {
		if len(c.keyEnvs) > 0 && keyStatusText(c.keyEnvs) != "no key set" {
			return i
		}
	}
	for i, c := range choices {
		if c.sc.Provider == summarize.ProviderOllama && c.ask == "" {
			return i
		}
	}
	return 0
}

func setupSummarize(ctx context.Context, ui setupUI, cur config.SummarizeConfig) (config.SummarizeConfig, error) {
	choices := summaryChoices()
	rows := make([]string, len(choices))
	for i, c := range choices {
		status := ""
		if len(c.keyEnvs) > 0 {
			status = " · " + keyStatusText(c.keyEnvs)
		}
		rows[i] = fmt.Sprintf("%-26s %s%s", c.label, c.detail, status)
	}
	def := defaultChoice(choices, cur)
	for {
		i, err := ui.choose("Which model summarizes your sessions?", "Transcript excerpts are sent to it. Cheap cloud models need a key; Ollama models run here.", rows, def)
		if err != nil {
			return cur, err
		}
		choice := choices[i]
		sc := choice.sc
		switch choice.ask {
		case "model":
			if sc.Model, err = ui.ask("Ollama model", "The name Ollama uses, for example qwen3.8:27b-mlx.", "", requiredValue("a model name")); err != nil {
				return cur, err
			}
			sc.BaseURL = ollamaBase()
		case "url":
			if sc.BaseURL, err = ui.ask("Server URL", "The base URL, usually ending in /v1.", "http://localhost:1234/v1", requiredValue("a URL")); err != nil {
				return cur, err
			}
			if sc.Model, err = ui.ask("Model name", "The name the server uses for the model.", "", requiredValue("a model name")); err != nil {
				return cur, err
			}
		case "command":
			if sc.Command, err = ui.ask("Command", "Receives the prompt on stdin and prints the summary.", "claude -p", requiredValue("a command")); err != nil {
				return cur, err
			}
		}
		if sc.Provider == summarize.ProviderOllama {
			sc.BaseURL = ollamaBase()
		}
		testErr := error(nil)
		if len(choice.keyEnvs) > 0 {
			if sc.Provider == summarize.ProviderAnthropic {
				sc.BaseURL = firstNonEmptyString(getenv("ANTHROPIC_BASE_URL"), sc.BaseURL)
			}
			sc.APIKeyEnv, testErr = testWithKey(ctx, ui, choice, sc)
		} else {
			testErr = ui.run("Testing "+choice.label, func() error { return testModel(ctx, sc) })
		}
		if testErr == nil {
			return sc, nil
		}
		if errors.Is(testErr, errAborted) {
			return cur, errAborted
		}
		ui.note(red("✗ " + oneLineErr(testErr)))
		again, err := ui.confirm("Choose another model?", "No saves this one anyway.", true)
		if err != nil || !again {
			return sc, err
		}
		cur = sc
	}
}

// testWithKey tries the variables that hold a key in this shell, in order, and
// returns the first that works. With none set, it asks for the variable's name.
func testWithKey(ctx context.Context, ui setupUI, choice modelChoice, sc config.SummarizeConfig) (string, error) {
	var set []string
	for _, e := range choice.keyEnvs {
		if getenv(e) != "" {
			set = append(set, e)
		}
	}
	if len(set) == 0 {
		name, err := askKeyEnv(ui, "", true, choice.keyEnvs...)
		if err != nil {
			return "", err
		}
		sc.APIKeyEnv = name
		return name, ui.run("Testing "+choice.label, func() error { return testModel(ctx, sc) })
	}
	var err error
	for _, e := range set {
		sc.APIKeyEnv = e
		err = ui.run("Testing "+choice.label+" with $"+e, func() error { return testModel(ctx, sc) })
		if err == nil {
			return e, nil
		}
	}
	return set[len(set)-1], err
}

func requiredValue(what string) func(string) error {
	return func(v string) error {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s is required", what)
		}
		return nil
	}
}

// ollamaBase is the Ollama URL setup saves: OLLAMA_HOST or the default.
func ollamaBase() string {
	return firstNonEmptyString(ollamaHost(), "http://localhost:11434")
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
	switch {
	case def != "":
	case len(set) > 0:
		def = set[0]
	case needed && len(candidates) > 0:
		def = candidates[0]
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
		ui.note(gray("$" + name + " is not set in this shell; set it before running summarize."))
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
	ui := newTermUI(cmd)
	defer ui.close()
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

func green(s string) string { return ansiGreen + s + ansiReset }
func red(s string) string   { return ansiRed + s + ansiReset }
func gray(s string) string  { return ansiGray + s + ansiReset }
