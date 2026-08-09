package imessage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

var ErrRPCUnsupported = errors.New("imsg RPC is unsupported")

// IMsgRPCSender keeps imsg's JSON-RPC process alive across replies. Requests
// are serialized so a lost response is treated as an ambiguous send instead
// of being retried and possibly delivered twice.
type IMsgRPCSender struct {
	argv []string
	env  []string

	mu          sync.Mutex
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	records     chan imsgRPCRecord
	done        chan error
	stderr      *lockedBuffer
	unsupported bool
	nextID      uint64
}

type imsgRPCRecord struct {
	ID     string          `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func NewIMsgRPCSender(cfg Config) *IMsgRPCSender {
	return &IMsgRPCSender{argv: []string{cfg.ImsgPath, "rpc"}}
}

func (s *IMsgRPCSender) Send(ctx context.Context, chatID, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unsupported {
		return ErrRPCUnsupported
	}
	if err := s.prepareLocked(); err != nil {
		return err
	}

	s.nextID++
	id := fmt.Sprintf("context-drop-send-%d", s.nextID)
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "send",
		"params":  map[string]any{"chat_id": chatID, "text": text},
	}
	if err := s.writeLocked(request); err != nil {
		s.stopLocked()
		return err
	}
	for {
		record, err := s.nextLocked(ctx)
		if err != nil {
			if watchUnsupported(s.stderrText()) {
				s.unsupported = true
				err = fmt.Errorf("%w: %v", ErrRPCUnsupported, err)
			}
			s.stopLocked()
			return err
		}
		if record.ID != id {
			continue
		}
		if record.Error != nil {
			return fmt.Errorf("JSON-RPC error %d: %s", record.Error.Code, record.Error.Message)
		}
		var result struct {
			OK bool `json:"ok"`
		}
		if err := json.Unmarshal(record.Result, &result); err != nil {
			return fmt.Errorf("decode imsg RPC send result: %w", err)
		}
		if !result.OK {
			return errors.New("imsg RPC send did not confirm delivery")
		}
		return nil
	}
}

func (s *IMsgRPCSender) prepareLocked() error {
	if s.cmd != nil {
		select {
		case <-s.done:
			s.stopLocked()
		default:
			return nil
		}
	}
	if len(s.argv) == 0 {
		return errors.New("imsg RPC executable is not configured")
	}
	cmd := exec.Command(s.argv[0], s.argv[1:]...)
	if s.env != nil {
		cmd.Env = s.env
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create imsg RPC stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("create imsg RPC stdout: %w", err)
	}
	stderr := &lockedBuffer{limit: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start imsg RPC: %w", err)
	}
	s.cmd = cmd
	s.stdin = stdin
	s.records = make(chan imsgRPCRecord, 16)
	s.done = make(chan error, 1)
	s.stderr = stderr
	go readIMsgRPC(stdout, s.records)
	done := s.done
	go func() { done <- cmd.Wait() }()
	return nil
}

func readIMsgRPC(stdout io.Reader, records chan<- imsgRPCRecord) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		var record imsgRPCRecord
		if json.Unmarshal(scanner.Bytes(), &record) == nil {
			records <- record
		}
	}
}

func (s *IMsgRPCSender) writeLocked(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := s.stdin.Write(data); err != nil {
		return fmt.Errorf("write imsg RPC request: %w", err)
	}
	return nil
}

func (s *IMsgRPCSender) nextLocked(ctx context.Context) (imsgRPCRecord, error) {
	select {
	case <-ctx.Done():
		return imsgRPCRecord{}, ctx.Err()
	case err := <-s.done:
		if err == nil {
			err = errors.New("process exited")
		}
		detail := strings.TrimSpace(s.stderrText())
		if detail != "" {
			return imsgRPCRecord{}, fmt.Errorf("imsg RPC: %w: %s", err, detail)
		}
		return imsgRPCRecord{}, fmt.Errorf("imsg RPC: %w", err)
	case record := <-s.records:
		return record, nil
	}
}

func (s *IMsgRPCSender) stderrText() string {
	if s.stderr == nil {
		return ""
	}
	return s.stderr.String()
}

func (s *IMsgRPCSender) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	return nil
}

func (s *IMsgRPCSender) stopLocked() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	s.cmd = nil
	s.stdin = nil
	s.records = nil
	s.done = nil
	s.stderr = nil
}
