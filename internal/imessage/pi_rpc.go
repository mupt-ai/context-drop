package imessage

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

//go:embed pi_context_filter.mjs
var piContextFilter []byte

//go:embed pi_router_extension.mjs
var piRouterExtension []byte

type PiRPCResponder struct {
	dir                 string
	argv                []string
	env                 []string
	contextFilterPath   string
	routerExtensionPath string

	mu             sync.Mutex
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	records        chan rpcRecord
	done           chan error
	stderr         *lockedBuffer
	needsBootstrap bool
	nextID         atomic.Uint64
}

type rpcRecord struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Command string          `json:"command,omitempty"`
	Success bool            `json:"success,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Message json.RawMessage `json:"message,omitempty"`

	AssistantMessageEvent struct {
		Type  string `json:"type"`
		Delta string `json:"delta,omitempty"`
	} `json:"assistantMessageEvent,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
}

type lockedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buf.Len()
	if remaining > len(p) {
		remaining = len(p)
	}
	if remaining > 0 {
		_, _ = b.buf.Write(p[:remaining])
	}
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// NewPiRPCResponder returns a long-lived responder only for trusted Pi commands
// that already identify a persistent session. Other explicit responder commands
// retain the one-process-per-message execution path.
func NewPiRPCResponder(cfg Config) (*PiRPCResponder, bool, error) {
	argv, ok := piRPCArgv(cfg.ResponderCommand)
	if !cfg.Trusted || !ok {
		if cfg.RouterMode {
			return nil, false, fmt.Errorf("router mode requires a trusted persistent Pi session")
		}
		return nil, false, nil
	}
	dir, _, err := Paths()
	if err != nil {
		return nil, false, err
	}
	contextFilterPath := filepath.Join(dir, "pi-context-filter.mjs")
	routerExtensionPath := ""
	if cfg.RouterMode {
		argv = restrictedRouterArgv(argv)
		routerExtensionPath = filepath.Join(dir, "pi-router-extension.mjs")
		argv = append(argv, "--no-builtin-tools", "--no-extensions", "--no-skills", "--extension", routerExtensionPath)
	}
	argv = append(argv, "--extension", contextFilterPath)
	return &PiRPCResponder{dir: cfg.ResponderCwd, argv: argv, contextFilterPath: contextFilterPath, routerExtensionPath: routerExtensionPath}, true, nil
}

func restrictedRouterArgv(argv []string) []string {
	out := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "--tools", "-t", "--exclude-tools", "-xt", "--extension", "-e":
			i++
		case "--no-tools", "-nt", "--no-builtin-tools", "-nbt", "--no-extensions", "-ne", "--no-skills", "-ns":
		default:
			if strings.HasPrefix(arg, "--tools=") || strings.HasPrefix(arg, "--exclude-tools=") || strings.HasPrefix(arg, "--extension=") {
				continue
			}
			out = append(out, arg)
		}
	}
	return out
}

func piRPCArgv(command []string) ([]string, bool) {
	if len(command) == 0 || filepath.Base(command[0]) != "pi" {
		return nil, false
	}
	hasSession := false
	args := []string{command[0], "--mode", "rpc"}
	for i := 1; i < len(command); i++ {
		arg := command[i]
		switch arg {
		case "--print", "-p":
			continue
		case "--mode":
			i++
			continue
		case "--session", "--session-id", "--session-dir":
			if i+1 >= len(command) {
				return nil, false
			}
			if arg == "--session" || arg == "--session-id" {
				hasSession = true
			}
			args = append(args, arg, command[i+1])
			i++
			continue
		}
		if strings.HasPrefix(arg, "--mode=") || strings.Contains(arg, "{prompt_file}") {
			continue
		}
		args = append(args, arg)
	}
	return args, hasSession
}

func (r *PiRPCResponder) SetDelegationEnv(url, capability, chatID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.env = append(os.Environ(), "CONTEXT_DROP_DELEGATE_URL="+url, "CONTEXT_DROP_DELEGATE_CAPABILITY="+capability, "CONTEXT_DROP_CHAT_ID="+chatID)
}

func (r *PiRPCResponder) Prepare(ctx context.Context) (PersistentResponderState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		select {
		case <-r.done:
			r.stopLocked()
		default:
			return PersistentResponderState{NeedsBootstrap: r.needsBootstrap}, nil
		}
	}
	started := time.Now()
	if err := r.start(ctx); err != nil {
		return PersistentResponderState{}, err
	}
	return PersistentResponderState{NeedsBootstrap: r.needsBootstrap, Startup: time.Since(started), ColdStart: true}, nil
}

func (r *PiRPCResponder) start(ctx context.Context) error {
	if r.contextFilterPath != "" {
		if err := writePrivateAsset(r.contextFilterPath, piContextFilter); err != nil {
			return fmt.Errorf("install Pi RPC context filter: %w", err)
		}
	}
	if r.routerExtensionPath != "" {
		if err := writePrivateAsset(r.routerExtensionPath, piRouterExtension); err != nil {
			return fmt.Errorf("install Pi router extension: %w", err)
		}
	}
	cmd := exec.Command(r.argv[0], r.argv[1:]...)
	cmd.Dir = r.dir
	if r.env != nil {
		cmd.Env = r.env
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create Pi RPC stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create Pi RPC stdout: %w", err)
	}
	stderr := &lockedBuffer{limit: 16 * 1024}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Pi RPC responder: %w", err)
	}
	r.cmd = cmd
	r.stdin = stdin
	r.records = make(chan rpcRecord, 64)
	r.done = make(chan error, 1)
	r.stderr = stderr
	go r.read(stdout, r.records)
	done := r.done
	go func() { done <- cmd.Wait() }()

	id := r.requestID("state")
	if err := r.write(map[string]any{"id": id, "type": "get_state"}); err != nil {
		r.stopLocked()
		return err
	}
	for {
		record, err := r.next(ctx)
		if err != nil {
			r.stopLocked()
			return fmt.Errorf("initialize Pi RPC responder: %w", err)
		}
		if record.Type != "response" || record.ID != id {
			continue
		}
		if !record.Success {
			r.stopLocked()
			return fmt.Errorf("initialize Pi RPC responder: %s", rpcError(record))
		}
		var state struct {
			MessageCount int `json:"messageCount"`
		}
		if err := json.Unmarshal(record.Data, &state); err != nil {
			r.stopLocked()
			return fmt.Errorf("decode Pi RPC state: %w", err)
		}
		r.needsBootstrap = state.MessageCount == 0
		return nil
	}
}

func writePrivateAsset(path string, data []byte) error {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return os.Chmod(path, 0o600)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".pi-context-filter-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (r *PiRPCResponder) read(stdout io.Reader, records chan<- rpcRecord) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var record rpcRecord
		if json.Unmarshal(scanner.Bytes(), &record) == nil {
			records <- record
		}
	}
}

func (r *PiRPCResponder) Respond(ctx context.Context, prompt string, maxOutput int) (Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil {
		return Response{}, errors.New("Pi RPC responder was not prepared")
	}

	id := r.requestID("prompt")
	started := time.Now()
	if err := r.write(map[string]any{"id": id, "type": "prompt", "message": prompt}); err != nil {
		r.stopLocked()
		return Response{}, err
	}
	accepted := false
	firstOutput := time.Duration(0)
	var reply string
	toolStarts := map[string]time.Time{}
	var toolDuration time.Duration
	var compactionStarted time.Time
	var compactionDuration time.Duration
	var turnStarted time.Time
	var modelRounds []ModelRoundMetrics
	for {
		record, err := r.next(ctx)
		if err != nil {
			if ctx.Err() != nil && r.abortAndDrain(time.Second) {
				return Response{}, fmt.Errorf("Pi RPC responder: %w", ctx.Err())
			}
			r.stopLocked()
			return Response{}, fmt.Errorf("Pi RPC responder: %w", err)
		}
		switch record.Type {
		case "response":
			if record.ID == id {
				if !record.Success {
					return Response{}, fmt.Errorf("Pi RPC prompt rejected: %s", rpcError(record))
				}
				accepted = true
			}
		case "message_update":
			if firstOutput == 0 && record.AssistantMessageEvent.Type == "text_delta" && record.AssistantMessageEvent.Delta != "" {
				firstOutput = time.Since(started)
			}
		case "message_end":
			if text := assistantText(record.Message); text != "" {
				reply = text
				if firstOutput == 0 {
					firstOutput = time.Since(started)
				}
			}
		case "turn_start":
			turnStarted = time.Now()
		case "turn_end":
			if !turnStarted.IsZero() {
				round := assistantRound(record.Message)
				round.Duration = time.Since(turnStarted)
				modelRounds = append(modelRounds, round)
				turnStarted = time.Time{}
			}
		case "tool_execution_start":
			toolStarts[record.ToolCallID] = time.Now()
		case "tool_execution_end":
			if toolStarted, ok := toolStarts[record.ToolCallID]; ok {
				toolDuration += time.Since(toolStarted)
				delete(toolStarts, record.ToolCallID)
			}
		case "compaction_start":
			compactionStarted = time.Now()
		case "compaction_end":
			if !compactionStarted.IsZero() {
				compactionDuration += time.Since(compactionStarted)
				compactionStarted = time.Time{}
			}
		case "agent_settled":
			if !accepted {
				return Response{}, errors.New("Pi RPC agent settled before accepting the prompt")
			}
			reply = strings.TrimSpace(reply)
			if reply == "" {
				return Response{}, errors.New("Pi RPC responder returned an empty reply")
			}
			if len(reply) > maxOutput {
				return Response{}, fmt.Errorf("Pi RPC responder reply exceeds %d bytes", maxOutput)
			}
			r.needsBootstrap = false
			return Response{Reply: reply, Metrics: ResponseMetrics{Responder: time.Since(started), TimeToFirstOutput: firstOutput, ToolExecution: toolDuration, Compaction: compactionDuration, ModelRounds: modelRounds}}, nil
		}
	}
}

func assistantRound(raw json.RawMessage) ModelRoundMetrics {
	var message struct {
		Role          string `json:"role"`
		Model         string `json:"model"`
		ResponseModel string `json:"responseModel"`
		ResponseID    string `json:"responseId"`
		Usage         struct {
			TotalTokens int64 `json:"totalTokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &message) != nil || message.Role != "assistant" {
		return ModelRoundMetrics{}
	}
	model := message.ResponseModel
	if model == "" {
		model = message.Model
	}
	return ModelRoundMetrics{Model: model, ResponseID: message.ResponseID, TotalTokens: message.Usage.TotalTokens}
}

func assistantText(raw json.RawMessage) string {
	var message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &message) != nil || message.Role != "assistant" {
		return ""
	}
	var text string
	if json.Unmarshal(message.Content, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(message.Content, &blocks) != nil {
		return ""
	}
	var out strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			out.WriteString(block.Text)
		}
	}
	return out.String()
}

func (r *PiRPCResponder) requestID(prefix string) string {
	return fmt.Sprintf("context-drop-%s-%d", prefix, r.nextID.Add(1))
}

func (r *PiRPCResponder) write(value any) error {
	if r.stdin == nil {
		return errors.New("Pi RPC stdin is unavailable")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := r.stdin.Write(data); err != nil {
		return fmt.Errorf("write Pi RPC command: %w", err)
	}
	return nil
}

func (r *PiRPCResponder) next(ctx context.Context) (rpcRecord, error) {
	select {
	case <-ctx.Done():
		return rpcRecord{}, ctx.Err()
	case err := <-r.done:
		if err == nil {
			err = errors.New("process exited")
		}
		detail := strings.TrimSpace(r.stderr.String())
		if len(detail) > 512 {
			detail = detail[len(detail)-512:]
		}
		if detail != "" {
			return rpcRecord{}, fmt.Errorf("%w: %s", err, detail)
		}
		return rpcRecord{}, err
	case record := <-r.records:
		return record, nil
	}
}

// abortAndDrain cooperatively cancels Pi's active operation and consumes its
// remaining events so the warm RPC stream is safe for the next message. Pi
// propagates abort to active tools, which is essential because killing only the
// long-lived Pi parent can leave shell descendants running.
func (r *PiRPCResponder) abortAndDrain(timeout time.Duration) bool {
	id := r.requestID("abort")
	if err := r.write(map[string]any{"id": id, "type": "abort"}); err != nil {
		return false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	acknowledged := false
	settled := false
	for {
		select {
		case <-timer.C:
			return false
		case <-r.done:
			return false
		case record := <-r.records:
			switch record.Type {
			case "response":
				if record.ID == id {
					if !record.Success {
						return false
					}
					acknowledged = true
				}
			case "agent_settled":
				settled = true
			}
			if acknowledged && settled {
				return true
			}
		}
	}
}

func rpcError(record rpcRecord) string {
	if len(record.Error) == 0 {
		return "unknown error"
	}
	var text string
	if json.Unmarshal(record.Error, &text) == nil {
		return text
	}
	return string(record.Error)
}

func (r *PiRPCResponder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopLocked()
	return nil
}

func (r *PiRPCResponder) stopLocked() {
	if r.cmd != nil && r.cmd.Process != nil {
		_ = r.cmd.Process.Signal(syscall.SIGTERM)
	}
	if r.stdin != nil {
		_ = r.stdin.Close()
	}
	r.cmd = nil
	r.stdin = nil
	r.records = nil
	r.done = nil
	r.stderr = nil
	r.needsBootstrap = false
}
