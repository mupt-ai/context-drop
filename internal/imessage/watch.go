package imessage

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

var ErrWatchUnsupported = errors.New("imsg watch is unsupported")

type MessageWatcher interface {
	Watch(context.Context, int64, func(Message) error) error
}

// ExecMessageWatcher keeps one imsg process attached to chat.db instead of
// spawning a history query for every receive interval.
type ExecMessageWatcher struct {
	Config Config
}

func (w ExecMessageWatcher) Watch(ctx context.Context, sinceRowID int64, handle func(Message) error) error {
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := []string{"watch", "--chat-id", w.Config.ChatID, "--debounce", w.Config.PollInterval().String(), "--attachments", "--convert-attachments", "--json"}
	if sinceRowID > 0 {
		args = append(args, "--since-rowid", strconv.FormatInt(sinceRowID, 10))
	}
	cmd := exec.CommandContext(watchCtx, w.Config.ImsgPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open imsg watch stdout: %w", err)
	}
	var stderr limitedBuffer
	stderr.Limit = 4096
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start imsg watch: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	var handleErr error
	for scanner.Scan() {
		messages, parseErr := ParseMessages(scanner.Bytes())
		if parseErr != nil {
			if watchUnsupported(string(scanner.Bytes())) {
				handleErr = fmt.Errorf("%w: %s", ErrWatchUnsupported, strings.TrimSpace(string(scanner.Bytes())))
			} else {
				handleErr = parseErr
			}
			break
		}
		for _, message := range messages {
			if handleErr = handle(message); handleErr != nil {
				break
			}
		}
		if handleErr != nil {
			break
		}
	}
	if scanErr := scanner.Err(); handleErr == nil && scanErr != nil {
		handleErr = fmt.Errorf("read imsg watch stream: %w", scanErr)
	}
	cancel()
	waitErr := cmd.Wait()
	if handleErr != nil {
		return handleErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if waitErr != nil {
		if watchUnsupported(stderr.String()) {
			return fmt.Errorf("%w: %s", ErrWatchUnsupported, strings.TrimSpace(stderr.String()))
		}
		return commandError("imsg watch", waitErr, []byte(stderr.String()))
	}
	return errors.New("imsg watch exited unexpectedly")
}

func watchUnsupported(stderr string) bool {
	stderr = strings.ToLower(stderr)
	return strings.Contains(stderr, "unknown command") || strings.Contains(stderr, "unknown subcommand") || strings.Contains(stderr, "unrecognized command") || strings.Contains(stderr, "unrecognized subcommand")
}
