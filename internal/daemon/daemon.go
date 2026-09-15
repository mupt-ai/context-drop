package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"contextdrop.dev/context-drop/internal/imessage"
	"contextdrop.dev/context-drop/internal/localhome"
	"contextdrop.dev/context-drop/internal/orchestrator"
	"contextdrop.dev/context-drop/internal/runtimeclient"
)

const (
	TickInterval                    = 10 * time.Second
	DefaultMessageWatchRetryMin     = time.Second
	DefaultMessageWatchRetryMax     = 30 * time.Second
	DefaultMessageWatchFailureLimit = 3
)

type PIDInfo struct {
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	Executable string    `json:"executable"`
	StartToken string    `json:"start_token"`
}
type Status struct {
	PID                  int                    `json:"pid,omitempty"`
	Alive                bool                   `json:"alive"`
	RuntimeHealthy       bool                   `json:"runtime_healthy"`
	Installed            bool                   `json:"service_installed"`
	Loaded               bool                   `json:"service_loaded"`
	ScheduleCount        int                    `json:"schedule_count"`
	EnabledScheduleCount int                    `json:"enabled_schedule_count"`
	JobCount             int                    `json:"job_count"`
	LastRuntimeError     string                 `json:"last_runtime_error,omitempty"`
	IMessageConfigured   bool                   `json:"imessage_configured"`
	IMessageEnabled      bool                   `json:"imessage_enabled"`
	IMessageInitialized  bool                   `json:"imessage_initialized"`
	LastMessagePollAt    *time.Time             `json:"last_message_poll_at,omitempty"`
	LastMessageError     string                 `json:"last_message_error,omitempty"`
	Workers              []runtimeclient.Worker `json:"workers,omitempty"`
	Runs                 []runtimeclient.Run    `json:"runs,omitempty"`
}

type RuntimeLauncher interface {
	LaunchManagedSchedule(context.Context, string, string, string, string, string, string, string, string) (runtimeclient.ManagedTask, error)
	Tasks(context.Context, string) ([]runtimeclient.ManagedTask, error)
}

type DelegationRuntime interface {
	Health(context.Context) error
	IssueRouterCapability(context.Context, string, string) (string, error)
	LeaseReport(context.Context, string, string) (runtimeclient.ParentReport, bool, error)
	FinishReport(context.Context, runtimeclient.ParentReport, string, string, bool) error
}

type Runner struct {
	Store                    orchestrator.Store
	Notifier                 orchestrator.Notifier
	Now                      func() time.Time
	Runtime                  RuntimeLauncher
	Delegation               DelegationRuntime
	IMessage                 *imessage.Adapter
	MessagePollInterval      time.Duration
	MessageWatchRetryMin     time.Duration
	MessageWatchRetryMax     time.Duration
	MessageWatchFailureLimit int
	mu                       sync.Mutex
	routerMu                 sync.RWMutex
	routerCapability         string
	messagePollMu            sync.Mutex
	messageWorkerOnce        sync.Once
	messageQueue             chan messageBatch
	messageDebounce          time.Duration
	commandWorkers           sync.WaitGroup
}

type messageBatch struct {
	messages []imessage.Message
	done     chan struct{}
}

func Paths() (dir, pid, logPath string, err error) {
	root, err := localhome.Root()
	if err != nil {
		return "", "", "", err
	}
	dir = filepath.Join(root, "daemon")
	return dir, filepath.Join(dir, "daemon.pid"), filepath.Join(dir, "daemon.log"), nil
}

func readPID() (PIDInfo, error) {
	_, path, _, err := Paths()
	if err != nil {
		return PIDInfo{}, err
	}
	var info PIDInfo
	data, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	err = json.Unmarshal(data, &info)
	return info, err
}

var processInspector = inspectProcess
var signalPID = func(pid int, signal os.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(signal)
}

func canonicalExecutable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}
	return path, nil
}

// activeDaemon treats the held process lock as the primary liveness signal and
// the PID identity as the authorization required to report or signal it.
func activeDaemon() (PIDInfo, bool, error) {
	dir, pidPath, _, err := Paths()
	if err != nil {
		return PIDInfo{}, false, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return PIDInfo{}, false, err
	}
	lock, lockErr := orchestrator.TryLock(filepath.Join(dir, "process.lock"))
	if lockErr == nil {
		_ = lock.Close()
		_ = os.Remove(pidPath)
		return PIDInfo{}, false, nil
	}
	if !errors.Is(lockErr, orchestrator.ErrLocked) {
		return PIDInfo{}, false, lockErr
	}
	info, err := readPID()
	if err != nil {
		return PIDInfo{}, false, fmt.Errorf("daemon lock is held but PID identity is unavailable: %w", err)
	}
	identity, err := processInspector(info.PID)
	if err != nil {
		return PIDInfo{}, false, fmt.Errorf("daemon lock is held but process identity cannot be verified: %w", err)
	}
	executable, err := canonicalExecutable()
	if err != nil {
		return PIDInfo{}, false, err
	}
	if info.Executable == "" || info.StartToken == "" || identity.Executable != executable || info.Executable != executable || identity.StartToken != info.StartToken {
		return PIDInfo{}, false, fmt.Errorf("daemon lock is held but PID %d does not match the recorded Context Drop process", info.PID)
	}
	return info, true, nil
}

func writePID(info PIDInfo) error {
	dir, path, _, err := Paths()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, _ := json.Marshal(info)
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func claimPID() (func(), error) {
	dir, path, _, err := Paths()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	processLock, err := orchestrator.TryLock(filepath.Join(dir, "process.lock"))
	if errors.Is(err, orchestrator.ErrLocked) {
		if info, readErr := readPID(); readErr == nil {
			return nil, fmt.Errorf("Context Drop daemon is already running (pid %d)", info.PID)
		}
		return nil, fmt.Errorf("Context Drop daemon is already running")
	}
	if err != nil {
		return nil, err
	}
	executable, err := canonicalExecutable()
	if err != nil {
		_ = processLock.Close()
		return nil, err
	}
	identity, err := inspectProcess(os.Getpid())
	if err != nil {
		_ = processLock.Close()
		return nil, fmt.Errorf("inspect daemon process identity: %w", err)
	}
	if err := writePID(PIDInfo{PID: os.Getpid(), StartedAt: time.Now().UTC(), Executable: executable, StartToken: identity.StartToken}); err != nil {
		_ = processLock.Close()
		return nil, err
	}
	return func() {
		if info, err := readPID(); err == nil && info.PID == os.Getpid() {
			_ = os.Remove(path)
		}
		_ = processLock.Close()
	}, nil
}

func NewRunner() (*Runner, error) {
	store, err := orchestrator.NewStore()
	if err != nil {
		return nil, err
	}
	client, err := runtimeclient.New()
	if err != nil {
		return nil, err
	}
	runner := &Runner{messageDebounce: 2 * time.Second, Store: store, Notifier: orchestrator.LocalNotifier{}, Now: func() time.Time { return time.Now().UTC() }, Runtime: client, Delegation: client}
	messageConfig, messageErr := imessage.Load()
	if messageErr == nil {
		adapter := &imessage.Adapter{Config: messageConfig}
		adapter.PersistentSender = imessage.NewIMsgRPCSender(messageConfig)
		responder, ok, responderErr := imessage.NewPiRPCResponder(messageConfig)
		if responderErr != nil {
			return nil, fmt.Errorf("configure persistent iMessage responder: %w", responderErr)
		}
		if ok {
			adapter.PersistentResponder = responder
		}
		adapter.Watcher = imessage.ExecMessageWatcher{Config: messageConfig}
		runner.IMessage = adapter
		runner.MessagePollInterval = messageConfig.PollInterval()
		runner.MessageWatchRetryMin = DefaultMessageWatchRetryMin
		runner.MessageWatchRetryMax = DefaultMessageWatchRetryMax
	} else if !errors.Is(messageErr, os.ErrNotExist) {
		return nil, fmt.Errorf("load iMessage config: %w", messageErr)
	}
	return runner, nil
}

func Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cleanup, err := claimPID()
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err := runtimeclient.Initialize(); err != nil {
		return err
	}
	runner, err := NewRunner()
	if err != nil {
		return err
	}
	if err := runner.Store.Update(func(st *orchestrator.State) error {
		for _, job := range st.Jobs {
			if job.ScheduleType == orchestrator.ScheduleCommand && job.Status == "running" && strings.HasPrefix(job.RuntimeRunID, "local:") {
				_ = orchestrator.SetJobStatus(st, job.ID, "failed", job.RuntimeRunID, "daemon restarted before script completion; not replayed", runner.Now())
			}
		}
		return nil
	}); err != nil {
		return err
	}
	defer func() { cancel(); runner.commandWorkers.Wait() }()
	if runner.IMessage != nil {
		defer runner.IMessage.Close()
	}
	childDone, stopChild, err := ensureRuntime(ctx)
	backoff := time.Second
	var retry <-chan time.Time
	if err != nil {
		log.Printf("Context Drop runtime unavailable: %v; retrying in %s", err, backoff)
		_ = recordRuntimeError(err)
		retry = time.After(backoff)
		backoff *= 2
		stopChild = func() {}
	} else {
		_ = recordRuntimeError(nil)
	}
	defer func() { stopChild() }()
	routerMode := runner.IMessage != nil && runner.IMessage.Config.RouterMode
	routerReady := !routerMode
	if routerMode && err == nil {
		if configureErr := runner.configureRouterWithRetry(ctx, 100, 100*time.Millisecond); configureErr != nil {
			stopChild()
			childDone = nil
			stopChild = func() {}
			_ = recordRuntimeError(configureErr)
			retry = time.After(backoff)
		} else {
			routerReady = true
		}
	}
	ticker := time.NewTicker(TickInterval)
	defer ticker.Stop()
	healthTicker := time.NewTicker(5 * time.Second)
	defer healthTicker.Stop()
	consecutiveHealthFailures := 0
	messageReceiverStarted := false
	startMessageReceiver := func() {
		if runner.IMessage != nil && runner.IMessage.Config.Enabled && routerReady && !messageReceiverStarted {
			messageReceiverStarted = true
			go func() {
				if runner.IMessage.PersistentResponder != nil {
					warmCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					_, warmErr := runner.IMessage.PersistentResponder.Prepare(warmCtx)
					cancel()
					if warmErr != nil {
						log.Printf("Context Drop main orchestrator warmup failed: %v", warmErr)
					}
				}
				runner.ReceiveMessages(ctx)
			}()
		}
	}
	startMessageReceiver()
	if runner.IMessage != nil && runner.IMessage.Config.Enabled {
		go runner.DeliverReports(ctx)
	}
	if err := runner.Tick(ctx); err != nil {
		log.Printf("Context Drop daemon tick: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case childErr, ok := <-childDone:
			if !ok {
				childErr = errors.New("runtime exited")
			}
			childDone = nil
			stopChild = func() {}
			log.Printf("Context Drop runtime exited unexpectedly: %v; restarting in %s", childErr, backoff)
			_ = recordRuntimeError(childErr)
			retry = time.After(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
		case <-retry:
			retry = nil
			childDone, stopChild, err = ensureRuntime(ctx)
			if err != nil {
				_ = recordRuntimeError(err)
				log.Printf("Context Drop runtime restart failed: %v; retrying in %s", err, backoff)
				retry = time.After(backoff)
				if backoff < 30*time.Second {
					backoff *= 2
				}
			} else {
				_ = recordRuntimeError(nil)
				if routerMode {
					if configureErr := runner.configureRouterWithRetry(ctx, 100, 100*time.Millisecond); configureErr != nil {
						stopChild()
						childDone = nil
						stopChild = func() {}
						_ = recordRuntimeError(configureErr)
						retry = time.After(backoff)
					} else {
						routerReady = true
						startMessageReceiver()
					}
				}
			}
		case <-healthTicker.C:
			client, clientErr := runtimeclient.New()
			if clientErr != nil {
				log.Printf("Context Drop runtime health setup failed: %v", clientErr)
				continue
			}
			healthCtx, cancel := context.WithTimeout(ctx, time.Second)
			healthErr := client.Health(healthCtx)
			cancel()
			if healthErr == nil {
				consecutiveHealthFailures = 0
				backoff = time.Second
				_ = recordRuntimeError(nil)
				if retry != nil {
					// Health recovered while a restart was pending. Cancel the
					// pending restart and keep the current owner. Router capability
					// setup is still required because this may be a different or
					// externally supervised runtime.
					retry = nil
					if routerMode && !routerReady {
						if configureErr := runner.configureRouterWithRetry(ctx, 100, 100*time.Millisecond); configureErr != nil {
							_ = recordRuntimeError(configureErr)
							retry = time.After(backoff)
						} else {
							routerReady = true
							startMessageReceiver()
						}
					}
				}
				continue
			}
			consecutiveHealthFailures++
			// Permit normal slow starts and transient stalls, but replace a supervised
			// child that stays alive yet unhealthy for three 5-second checks.
			if consecutiveHealthFailures >= 3 && retry == nil {
				log.Printf("Context Drop runtime remained unhealthy after %d checks: %v; restarting", consecutiveHealthFailures, healthErr)
				stopChild()
				childDone = nil
				stopChild = func() {}
				routerReady = !routerMode
				consecutiveHealthFailures = 0
				retry = time.After(backoff)
				if backoff < 30*time.Second {
					backoff *= 2
				}
			}
		case <-ticker.C:
			if err := runner.Tick(ctx); err != nil {
				log.Printf("Context Drop daemon tick: %v", err)
			}
		}
	}
}

func ensureRuntime(ctx context.Context) (<-chan error, func(), error) {
	client, err := runtimeclient.New()
	if err != nil {
		return nil, nil, err
	}
	for attempt := 0; ; attempt++ {
		healthCtx, cancel := context.WithTimeout(ctx, time.Second)
		healthErr := client.Health(healthCtx)
		cancel()
		if healthErr == nil {
			log.Printf("Context Drop daemon using existing healthy local runtime")
			return nil, func() {}, nil
		}
		live, ownerErr := runtimeWriterOwnerAlive()
		if ownerErr != nil {
			return nil, nil, ownerErr
		}
		if !live {
			break
		}
		// A process that owns the writer lock may be between lock acquisition and
		// listen(). Never start a duplicate while that owner is alive.
		if attempt >= 50 {
			return nil, nil, fmt.Errorf("runtime writer is alive but health is not ready yet")
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if conflictErr := runtimePortConflict(); conflictErr != nil {
		return nil, nil, conflictErr
	}
	return startRuntime(ctx)
}

func runtimeWriterOwnerAlive() (bool, error) {
	cfg, err := runtimeclient.LoadConfig()
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(filepath.Join(cfg.StateDir, "writer.lock", "pid"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return false, nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, nil
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission), nil
}

func runtimePortConflict() error {
	cfg, err := runtimeclient.LoadConfig()
	if err != nil {
		return err
	}
	connection, err := net.DialTimeout("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)), 300*time.Millisecond)
	if err != nil {
		return nil
	}
	_ = connection.Close()
	return fmt.Errorf("runtime port %s:%d is occupied by a process that did not authenticate as this Context Drop runtime", cfg.Host, cfg.Port)
}

func startRuntime(ctx context.Context) (<-chan error, func(), error) {
	if conflictErr := runtimePortConflict(); conflictErr != nil {
		return nil, nil, conflictErr
	}
	entry, err := runtimeclient.RuntimeEntry()
	if err != nil {
		return nil, nil, err
	}
	_, configPath, _, err := runtimeclient.Paths()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := runtimeclient.LoadConfig()
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.CommandContext(ctx, cfg.NodePath, entry, configPath)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); close(done) }()
	return done, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
	}, nil
}

// PollMessages processes one chat sequentially. A message ID is durably claimed
// before running the responder, giving at-most-once send semantics across daemon
// restarts. A crash after the claim can lose a reply, but cannot duplicate one.
func (r *Runner) PollMessages(ctx context.Context) {
	if r.IMessage == nil || !r.IMessage.Config.Enabled || !r.messagePollMu.TryLock() {
		return
	}
	pollLocked := true
	defer func() {
		if pollLocked {
			r.messagePollMu.Unlock()
		}
	}()
	now := r.Now()
	historyStarted := time.Now()
	messages, err := r.IMessage.ConversationHistory(ctx)
	historyDuration := time.Since(historyStarted)
	if err != nil {
		_ = r.recordMessagePoll(now, err)
		return
	}
	initialized := false
	claimedMessages := make([]imessage.Message, 0, len(messages))
	if err := r.Store.Update(func(st *orchestrator.State) error {
		st.LastMessagePollAt = &now
		st.LastMessageError = ""
		if st.IMessageChatID != r.IMessage.Config.ChatID {
			st.IMessageInitialized = false
			st.IMessageChatID = r.IMessage.Config.ChatID
			st.IMessageCursor = 0
			st.SeenMessageIDs = map[string]string{}
			st.MessageJobs = map[string]orchestrator.MessageJob{}
		}
		if !st.IMessageInitialized {
			if st.SeenMessageIDs == nil {
				st.SeenMessageIDs = map[string]string{}
			}
			for _, message := range messages {
				st.SeenMessageIDs[message.ID] = now.Format(time.RFC3339Nano)
				if message.FromMe {
					sentAt := now
					if created := parseMessageCreatedAt(message.CreatedAt); created != nil {
						sentAt = *created
					}
					orchestrator.RecordOutbound(st, message.ID, message.Text, sentAt, "imessage-history")
				}
				if rowID, rowErr := messageRowID(message); rowErr == nil && rowID > st.IMessageCursor {
					st.IMessageCursor = rowID
				}
			}
			st.IMessageInitialized = true
			initialized = true
			return nil
		}
		// History is a snapshot, so claim every unseen message in the same durable
		// transaction. Updating once per history row made an idle poll perform a
		// full state rewrite for every already-seen message.
		for _, message := range messages {
			if rowID, rowErr := messageRowID(message); rowErr == nil && rowID > st.IMessageCursor {
				st.IMessageCursor = rowID
			}
			if _, seen := st.SeenMessageIDs[message.ID]; seen {
				continue
			}
			claimTime := r.Now()
			createdAt := parseMessageCreatedAt(message.CreatedAt)
			st.SeenMessageIDs[message.ID] = claimTime.Format(time.RFC3339Nano)
			if message.FromMe {
				sentAt := claimTime
				if createdAt != nil {
					sentAt = *createdAt
				}
				orchestrator.RecordOutbound(st, message.ID, message.Text, sentAt, "imessage-history")
				continue
			}
			latency := orchestrator.MessageLatency{MessageCreatedAt: createdAt, HistoryMS: historyDuration.Milliseconds()}
			if createdAt != nil && !claimTime.Before(*createdAt) {
				latency.QueueMS = claimTime.Sub(*createdAt).Milliseconds()
			}
			st.SeenMessageIDs[message.ID] = claimTime.Format(time.RFC3339Nano)
			st.MessageJobs[message.ID] = orchestrator.MessageJob{Input: &orchestrator.MessageInput{Text: message.Text, ChatID: message.ChatID, CreatedAt: message.CreatedAt, Attachments: storedAttachments(message.Attachments)}, MessageID: message.ID, Status: "queued", ClaimedAt: claimTime, UpdatedAt: claimTime, Latency: latency}
			claimedMessages = append(claimedMessages, message)
		}
		return nil
	}); err != nil {
		log.Printf("Context Drop iMessage poll state update failed: %v", err)
		return
	}
	if initialized {
		log.Printf("Context Drop iMessage initial sync marked %d existing chat message(s) seen", len(messages))
		return
	}
	if len(claimedMessages) == 0 {
		return
	}

	// Enqueue under the poll lock so batches retain source order, then release
	// the poller immediately. The single session worker keeps model/tool work
	// sequential while later history polls continue claiming durable jobs.
	done, err := r.enqueueMessages(ctx, claimedMessages, true)
	if err != nil {
		return
	}
	r.messagePollMu.Unlock()
	pollLocked = false
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// ReceiveMessages prefers imsg's long-lived watch stream. Old imsg binaries
// that do not expose watch retain the history polling path.
func (r *Runner) ReceiveMessages(ctx context.Context) {
	for {
		if err := r.recoverMessages(ctx); err == nil {
			break
		} else {
			log.Printf("Context Drop iMessage recovery failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	err := r.WatchMessages(ctx)
	if ctx.Err() != nil {
		return
	}
	if errors.Is(err, imessage.ErrWatchUnsupported) {
		log.Printf("Context Drop iMessage watch unavailable; using history polling: %v", err)
	} else {
		log.Printf("Context Drop iMessage watch stopped; using history polling: %v", err)
	}
	interval := r.MessagePollInterval
	if interval <= 0 && r.IMessage != nil {
		interval = r.IMessage.Config.PollInterval()
	}
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var polls sync.WaitGroup
	launchPoll := func() {
		polls.Add(1)
		go func() {
			defer polls.Done()
			r.PollMessages(ctx)
		}()
	}
	launchPoll()
	for {
		select {
		case <-ctx.Done():
			polls.Wait()
			return
		case <-ticker.C:
			launchPoll()
		}
	}
}

func (r *Runner) WatchMessages(ctx context.Context) error {
	if r.IMessage == nil {
		return imessage.ErrWatchUnsupported
	}
	retryMin := r.MessageWatchRetryMin
	if retryMin <= 0 {
		retryMin = DefaultMessageWatchRetryMin
	}
	retryMax := r.MessageWatchRetryMax
	if retryMax < retryMin {
		retryMax = DefaultMessageWatchRetryMax
	}
	failureLimit := r.MessageWatchFailureLimit
	if failureLimit <= 0 {
		failureLimit = DefaultMessageWatchFailureLimit
	}
	backoff := retryMin
	consecutiveFailures := 0
	for {
		cursor, err := r.prepareMessageWatch(ctx)
		watchStarted := time.Time{}
		if err == nil {
			watchStarted = time.Now()
			now := r.Now()
			err = r.Store.Update(func(st *orchestrator.State) error {
				st.LastMessagePollAt = &now
				st.LastMessageError = ""
				return nil
			})
			if err == nil {
				log.Printf("Context Drop iMessage watch started after rowid %d", cursor)
				err = r.IMessage.Watch(ctx, cursor, func(message imessage.Message) error {
					return r.claimWatchedMessage(ctx, message)
				})
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, imessage.ErrWatchUnsupported) {
			return err
		}
		if err == nil {
			err = errors.New("iMessage watch stopped without an error")
		}
		if !watchStarted.IsZero() && time.Since(watchStarted) >= time.Minute {
			backoff = retryMin
			consecutiveFailures = 0
		}
		consecutiveFailures++
		if consecutiveFailures >= failureLimit {
			return fmt.Errorf("iMessage watch failed %d consecutive times; use history polling: %w", consecutiveFailures, err)
		}
		log.Printf("Context Drop iMessage watch failed: %v; restarting in %s", err, backoff)
		now := r.Now()
		_ = r.Store.Update(func(st *orchestrator.State) error {
			st.LastMessagePollAt = &now
			st.LastMessageError = err.Error()
			return nil
		})
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < retryMax {
			backoff *= 2
			if backoff > retryMax {
				backoff = retryMax
			}
		}
	}
}

func (r *Runner) prepareMessageWatch(ctx context.Context) (int64, error) {
	state, err := r.Store.Load()
	if err != nil {
		return 0, err
	}
	if state.IMessageChatID != r.IMessage.Config.ChatID || !state.IMessageInitialized {
		r.PollMessages(ctx)
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		state, err = r.Store.Load()
		if err != nil {
			return 0, err
		}
		if state.IMessageChatID != r.IMessage.Config.ChatID || !state.IMessageInitialized {
			return 0, errors.New("iMessage initial sync did not initialize the configured chat")
		}
	}
	syncCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if syncErr := r.syncRecentOutbound(syncCtx); syncErr != nil {
		log.Printf("Context Drop recent outbound sync failed: %v", syncErr)
	}
	cancel()
	cursor := state.IMessageCursor
	if cursor == 0 {
		cursor = maxSeenMessageRowID(state.SeenMessageIDs)
		if cursor > 0 {
			if err := r.Store.Update(func(st *orchestrator.State) error {
				if cursor > st.IMessageCursor {
					st.IMessageCursor = cursor
				}
				return nil
			}); err != nil {
				return 0, err
			}
		}
	}
	return cursor, nil
}

func (r *Runner) syncRecentOutbound(ctx context.Context) error {
	messages, err := r.IMessage.ConversationHistory(ctx)
	if err != nil {
		return err
	}
	return r.Store.Update(func(st *orchestrator.State) error {
		for _, message := range messages {
			if !message.FromMe {
				continue
			}
			sentAt := r.Now()
			if created := parseMessageCreatedAt(message.CreatedAt); created != nil {
				sentAt = *created
			}
			orchestrator.RecordOutbound(st, message.ID, message.Text, sentAt, "imessage-history")
		}
		return nil
	})
}

func (r *Runner) claimWatchedMessage(ctx context.Context, message imessage.Message) error {
	rowID, err := messageRowID(message)
	if err != nil {
		return err
	}
	chatMessage, accepted := r.IMessage.ChatMessage(message)
	claimTime := r.Now()
	claimed := false
	if err := r.Store.Update(func(st *orchestrator.State) error {
		if st.IMessageChatID != r.IMessage.Config.ChatID || !st.IMessageInitialized {
			return errors.New("iMessage watch state no longer matches the configured chat")
		}
		st.LastMessagePollAt = &claimTime
		st.LastMessageError = ""
		alreadyPassed := rowID <= st.IMessageCursor
		if !alreadyPassed {
			st.IMessageCursor = rowID
		}
		if alreadyPassed || !accepted {
			return nil
		}
		if _, seen := st.SeenMessageIDs[chatMessage.ID]; seen {
			return nil
		}
		createdAt := parseMessageCreatedAt(chatMessage.CreatedAt)
		st.SeenMessageIDs[chatMessage.ID] = claimTime.Format(time.RFC3339Nano)
		if chatMessage.FromMe {
			sentAt := claimTime
			if createdAt != nil {
				sentAt = *createdAt
			}
			orchestrator.RecordOutbound(st, chatMessage.ID, chatMessage.Text, sentAt, "imessage-watch")
			return nil
		}
		latency := orchestrator.MessageLatency{MessageCreatedAt: createdAt}
		if createdAt != nil && !claimTime.Before(*createdAt) {
			latency.QueueMS = claimTime.Sub(*createdAt).Milliseconds()
		}
		st.SeenMessageIDs[chatMessage.ID] = claimTime.Format(time.RFC3339Nano)
		st.MessageJobs[chatMessage.ID] = orchestrator.MessageJob{Input: &orchestrator.MessageInput{Text: chatMessage.Text, ChatID: chatMessage.ChatID, CreatedAt: chatMessage.CreatedAt, Attachments: storedAttachments(chatMessage.Attachments)}, MessageID: chatMessage.ID, Status: "queued", ClaimedAt: claimTime, UpdatedAt: claimTime, Latency: latency}
		claimed = true
		return nil
	}); err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	_, err = r.enqueueMessages(ctx, []imessage.Message{chatMessage}, false)
	return err
}

func messageRowID(message imessage.Message) (int64, error) {
	rowID, err := strconv.ParseInt(message.ID, 10, 64)
	if err != nil || rowID <= 0 {
		return 0, fmt.Errorf("imsg watch message has invalid rowid %q", message.ID)
	}
	return rowID, nil
}

func maxSeenMessageRowID(seen map[string]string) int64 {
	var cursor int64
	for id := range seen {
		rowID, err := strconv.ParseInt(id, 10, 64)
		if err == nil && rowID > cursor {
			cursor = rowID
		}
	}
	return cursor
}

func (r *Runner) enqueueMessages(ctx context.Context, messages []imessage.Message, wait bool) (<-chan struct{}, error) {
	r.startMessageWorker(ctx)
	var done chan struct{}
	if wait {
		done = make(chan struct{})
	}
	select {
	case r.messageQueue <- messageBatch{messages: messages, done: done}:
		return done, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *Runner) startMessageWorker(ctx context.Context) {
	r.messageWorkerOnce.Do(func() {
		r.messageQueue = make(chan messageBatch, 1024)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case batch := <-r.messageQueue:
					batches := []messageBatch{batch}
					if r.messageDebounce > 0 {
						quiet := time.NewTimer(r.messageDebounce)
						limit := time.NewTimer(3 * r.messageDebounce)
					collect:
						for {
							select {
							case next := <-r.messageQueue:
								batches = append(batches, next)
								if !quiet.Stop() {
									select {
									case <-quiet.C:
									default:
									}
								}
								quiet.Reset(r.messageDebounce)
							case <-quiet.C:
								break collect
							case <-limit.C:
								break collect
							case <-ctx.Done():
								quiet.Stop()
								limit.Stop()
								return
							}
						}
						quiet.Stop()
						limit.Stop()
					}
					var group []imessage.Message
					bytes := 0
					for _, next := range batches {
						for _, message := range next.messages {
							if len(group) > 0 && (r.messageDebounce == 0 || message.ChatID != group[0].ChatID || bytes+len(message.Text) > 16000) {
								r.processMessages(ctx, group)
								group = nil
								bytes = 0
							}
							group = append(group, message)
							bytes += len(message.Text) + 2
						}
					}
					if len(group) > 0 {
						r.processMessages(ctx, group)
					}
					for _, next := range batches {
						if next.done != nil {
							close(next.done)
						}
					}
				}
			}
		}()
	})
}

// Only queued work is safe to replay. A processing turn may already have
// executed tools or sent a reply, so preserve it for explicit reconciliation.
func (r *Runner) recoverMessages(ctx context.Context) error {
	var queued []orchestrator.MessageJob
	if err := r.Store.Update(func(st *orchestrator.State) error {
		if r.IMessage == nil || st.IMessageChatID != r.IMessage.Config.ChatID {
			return nil
		}
		for id, job := range st.MessageJobs {
			if job.Status == "processing" || (job.Status == "queued" && job.Input == nil) {
				job.Status = "unknown"
				job.Error = "interrupted request; inspect outcome before retrying"
				job.UpdatedAt = r.Now()
				st.MessageJobs[id] = job
			} else if job.Status == "queued" {
				queued = append(queued, job)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(queued, func(i, j int) bool {
		if queued[i].ClaimedAt.Equal(queued[j].ClaimedAt) {
			return queued[i].MessageID < queued[j].MessageID
		}
		return queued[i].ClaimedAt.Before(queued[j].ClaimedAt)
	})
	for _, job := range queued {
		input := job.Input
		if _, err := r.enqueueMessages(ctx, []imessage.Message{{ID: job.MessageID, Text: input.Text, ChatID: input.ChatID, CreatedAt: input.CreatedAt, Attachments: messageAttachments(input.Attachments)}}, false); err != nil {
			return err
		}
	}
	return nil
}

func responderFailureReply(err error, response imessage.Response) string {
	if response.SideEffectToolCompleted {
		return "the responder didn’t produce a final reply, but delegated or continued work may already have started. i won’t repeat the request; check current task status before deciding next steps."
	}
	if response.ToolCompleted {
		return "the responder completed at least one tool call but didn’t produce a final reply. i won’t repeat the request automatically; check current status before retrying anything."
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "the responder timed out before a final reply. work may have started, so i won’t repeat the request automatically; check current task status first."
	}
	var prePromptErr *imessage.ResponderPrePromptError
	if errors.As(err, &prePromptErr) {
		return "the responder failed before starting. no tools or side effects ran, so it’s safe to resend the request."
	}
	return "i couldn’t complete that responder turn. i won’t retry automatically because the outcome may be ambiguous; check current status before resending."
}

func (r *Runner) processMessage(ctx context.Context, message imessage.Message) {
	r.processMessages(ctx, []imessage.Message{message})
}

func (r *Runner) processMessages(ctx context.Context, messages []imessage.Message) {
	message := messages[0]
	texts := make([]string, 0, len(messages))
	message.Attachments = nil
	for _, item := range messages {
		// Grouped messages become one turn, so each one's attachments follow
		// its own text and all of them ride along as content.
		texts = append(texts, item.PromptText())
		message.Attachments = append(message.Attachments, item.Attachments...)
	}
	message.Text = strings.Join(texts, "\n\n")
	message.Rendered = true
	if state, err := r.Store.Load(); err == nil {
		message.RecentOutbound = make([]imessage.ContextMessage, 0, len(state.RecentOutbound))
		for _, outbound := range state.RecentOutbound {
			message.RecentOutbound = append(message.RecentOutbound, imessage.ContextMessage{Text: outbound.Text, CreatedAt: outbound.SentAt.Format(time.RFC3339), Source: outbound.Source})

		}
	}
	processingStarted := r.Now()
	if err := r.Store.Update(func(st *orchestrator.State) error {
		for _, item := range messages {
			job := st.MessageJobs[item.ID]
			job.Status = "processing"
			job.ProcessingStartedAt = &processingStarted
			job.UpdatedAt = processingStarted
			if !processingStarted.Before(job.ClaimedAt) {
				job.Latency.WorkerQueueMS = processingStarted.Sub(job.ClaimedAt).Milliseconds()
			}
			st.MessageJobs[item.ID] = job
		}
		return nil
	}); err != nil {
		log.Printf("Context Drop iMessage processing state failed: %v", err)
		return // Never execute tools unless the processing transition is durable.
	}
	var response imessage.Response
	var responderErr error
	response, responderErr = r.IMessage.RespondMeasured(ctx, message)
	processErr := responderErr
	if response.MessagingSideEffectToolCompleted {
		processErr = nil
	}
	var sendDuration time.Duration
	if processErr == nil && !response.ThreadReplyToolCompleted && response.Reply != "" {
		sendStarted := time.Now()
		processErr = r.IMessage.Send(ctx, response.Reply)
		sendDuration = time.Since(sendStarted)
	}
	completedAt := r.Now()
	status := "sent"
	if response.Reply == "" && !response.MessagingSideEffectToolCompleted {
		status = "handled"
	}
	errorText := ""
	if processErr != nil {
		status = "failed"
		errorText = processErr.Error()
		log.Printf("Context Drop iMessage message %s failed: %v", message.ID, processErr)
		// Only send a generic error when the responder failed. If `imsg send`
		// failed after possibly delivering, never send a second reply.
		if responderErr != nil && !response.MessagingSideEffectToolCompleted {
			_ = r.IMessage.Send(ctx, responderFailureReply(responderErr, response))
		}
	}
	if err := r.Store.Update(func(st *orchestrator.State) error {
		for _, item := range messages {
			job := st.MessageJobs[item.ID]
			job.Status = status
			job.Input = nil
			job.UpdatedAt = completedAt
			job.Error = errorText
			if status == "sent" {
				job.SentAt = &completedAt
			}
			job.Latency.PromptBuildMS = response.Metrics.PromptBuild.Milliseconds()
			job.Latency.ResponderStartupMS = response.Metrics.ResponderStartup.Milliseconds()
			job.Latency.ResponderMS = response.Metrics.Responder.Milliseconds()
			job.Latency.FirstOutputMS = response.Metrics.TimeToFirstOutput.Milliseconds()
			job.Latency.ToolExecutionMS = response.Metrics.ToolExecution.Milliseconds()
			job.Latency.CompactionMS = response.Metrics.Compaction.Milliseconds()
			job.Latency.SendMS = sendDuration.Milliseconds()
			job.Latency.ServiceMS = completedAt.Sub(job.ClaimedAt).Milliseconds()
			if job.Latency.MessageCreatedAt != nil && !completedAt.Before(*job.Latency.MessageCreatedAt) {
				job.Latency.EndToEndMS = completedAt.Sub(*job.Latency.MessageCreatedAt).Milliseconds()
			}
			job.Latency.PromptBytes = response.Metrics.PromptBytes
			job.Latency.ColdStart = response.Metrics.ColdStart
			job.Latency.ModelRounds = make([]orchestrator.ModelRoundLatency, 0, len(response.Metrics.ModelRounds))
			for _, round := range response.Metrics.ModelRounds {
				job.Latency.ModelRounds = append(job.Latency.ModelRounds, orchestrator.ModelRoundLatency{DurationMS: round.Duration.Milliseconds(), Model: round.Model, ResponseID: round.ResponseID, TotalTokens: round.TotalTokens})
			}
			st.MessageJobs[item.ID] = job
		}
		if status == "sent" && response.Reply != "" {
			orchestrator.RecordOutbound(st, "", response.Reply, completedAt, "conversation")
		}
		return nil
	}); err != nil {
		log.Printf("Context Drop iMessage completion state failed: %v", err)
	}
}

func parseMessageCreatedAt(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			parsed = parsed.UTC()
			return &parsed
		}
	}
	numeric, err := strconv.ParseFloat(value, 64)
	if err != nil || numeric <= 0 {
		return nil
	}
	seconds := numeric
	if numeric >= 1e12 {
		seconds = numeric / 1000
	}
	whole := int64(seconds)
	nanos := int64((seconds - float64(whole)) * float64(time.Second))
	parsed := time.Unix(whole, nanos).UTC()
	return &parsed
}

func (r *Runner) recordMessagePoll(now time.Time, pollErr error) error {
	log.Printf("Context Drop iMessage poll failed: %v", pollErr)
	return r.Store.Update(func(st *orchestrator.State) error {
		st.LastMessagePollAt = &now
		st.LastMessageError = pollErr.Error()
		return nil
	})
}

// ScheduleReportOwner binds managed schedule reports to the configured
// orchestrator destination while keeping a stable scheduler source identity.
func ScheduleReportOwner(cfg imessage.Config) (routerID, chatID string, err error) {
	if !cfg.Enabled || !cfg.RouterMode || strings.TrimSpace(cfg.ChatID) == "" {
		return "", "", fmt.Errorf("managed schedules require an enabled router-mode iMessage destination")
	}
	return scheduleRouterID, cfg.ChatID, nil
}

func (r *Runner) Tick(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.Now()
	var claims []orchestrator.Claim
	if err := r.Store.Update(func(st *orchestrator.State) error {
		for _, job := range st.Jobs {
			if job.Status != "queued" && !(job.Status == "running" && job.RuntimeRunID == "") {
				continue
			}
			for _, schedule := range st.Schedules {
				if schedule.Name == job.ScheduleName {
					claims = append(claims, orchestrator.Claim{Schedule: schedule, Job: job})
					break
				}
			}
		}
		claims = append(claims, orchestrator.ClaimDue(st, now)...)
		return nil
	}); err != nil {
		return err
	}
	for _, claim := range claims {
		if claim.Schedule.Type == orchestrator.ScheduleCommand {
			if err := r.Store.Update(func(st *orchestrator.State) error {
				return orchestrator.SetJobStatus(st, claim.Job.ID, "running", "local:"+claim.Job.ID, "", now)
			}); err != nil {
				return err
			}
			r.commandWorkers.Add(1)
			go func() { defer r.commandWorkers.Done(); r.ExecuteClaim(ctx, claim, nil, now) }()
		} else {
			r.ExecuteClaim(ctx, claim, nil, now)
		}
	}
	return nil
}

// Agent occurrences use the shared worker pool. Command occurrences execute locally.
func (r *Runner) ExecuteClaim(ctx context.Context, claim orchestrator.Claim, _ []runtimeclient.ManagedTask, now time.Time) {
	s, job := claim.Schedule, claim.Job
	if s.Type == orchestrator.ScheduleCommand {
		r.executeCommand(ctx, claim)
		return
	}
	if err := r.Store.Update(func(st *orchestrator.State) error {
		return orchestrator.SetJobStatus(st, job.ID, "running", "", "", now)
	}); err != nil {
		return
	}
	status, runID, detail := "running", "", ""
	var err error
	var routerID, chatID string
	if r.IMessage == nil {
		err = fmt.Errorf("schedules require the main orchestrator")
	} else {
		routerID, chatID, err = ScheduleReportOwner(r.IMessage.Config)
	}
	prompt, repo := s.Prompt, s.Repo
	switch s.Type {
	case orchestrator.ScheduleWatch:
		prompt = fmt.Sprintf("Scheduled task %s: inspect %s pane %s (task name %s), report its current state, and make no changes. Do not create further schedules.", s.Name, s.Backend, s.WatchPane, s.WatchTarget)
	}
	if err == nil {
		var task runtimeclient.ManagedTask
		task, err = r.Runtime.LaunchManagedSchedule(ctx, s.Agent, repo, prompt, "schedule-"+s.Name, "", routerID, chatID, job.ID)
		runID = task.RunID
	}
	if err != nil {
		status, detail = "queued", err.Error()
	}
	_ = r.Store.Update(func(st *orchestrator.State) error {
		return orchestrator.SetJobStatus(st, job.ID, status, runID, detail, now)
	})

}

func Start() (PIDInfo, error) {
	if info, active, err := activeDaemon(); err != nil {
		return PIDInfo{}, err
	} else if active {
		return info, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return PIDInfo{}, err
	}
	dir, _, logPath, err := Paths()
	if err != nil {
		return PIDInfo{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return PIDInfo{}, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return PIDInfo{}, err
	}
	defer logFile.Close()
	cmd := exec.Command(exe, "daemon", "run")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return PIDInfo{}, err
	}
	_ = cmd.Process.Release()
	info, err := waitForPID(5 * time.Second)
	if err != nil {
		return PIDInfo{}, fmt.Errorf("Context Drop daemon did not start; see %s: %w", logPath, err)
	}
	return info, nil
}

func waitForPID(timeout time.Duration) (PIDInfo, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if info, active, err := activeDaemon(); err == nil && active {
			return info, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return PIDInfo{}, fmt.Errorf("daemon PID did not become healthy within %s", timeout)
}

func Stop() error {
	info, active, err := activeDaemon()
	if err != nil {
		return err
	}
	if !active {
		return nil
	}
	if err := signalPID(info.PID, syscall.SIGTERM); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, active, _ := activeDaemon(); !active {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("Context Drop daemon pid %d did not stop", info.PID)
}

func CurrentStatus(ctx context.Context) (Status, error) {
	var out Status
	if info, active, err := activeDaemon(); err == nil && active {
		out.PID = info.PID
		out.Alive = true
	}
	if client, err := runtimeclient.New(); err == nil {
		healthCtx, cancel := context.WithTimeout(ctx, time.Second)
		out.RuntimeHealthy = client.Health(healthCtx) == nil
		cancel()
		if out.RuntimeHealthy {
			out.Runs, _ = client.Runs(ctx)
			out.Workers, _ = client.Workers(ctx)
		}
	}
	store, err := orchestrator.NewStore()
	if err != nil {
		return out, err
	}
	st, err := store.Load()
	if err != nil {
		return out, err
	}
	out.ScheduleCount = len(st.Schedules)
	for _, s := range st.Schedules {
		if s.Enabled {
			out.EnabledScheduleCount++
		}
	}
	out.JobCount = len(st.Jobs)
	out.LastRuntimeError = st.LastRuntimeError
	out.IMessageInitialized = st.IMessageInitialized
	out.LastMessagePollAt = st.LastMessagePollAt
	out.LastMessageError = st.LastMessageError
	if messageConfig, messageErr := imessage.Load(); messageErr == nil {
		out.IMessageConfigured = true
		out.IMessageEnabled = messageConfig.Enabled
	}
	service := ServiceStatus()
	out.Installed = service.Installed
	out.Loaded = service.Loaded
	return out, nil
}

func ReadLogs(lines int) (string, error) {
	_, _, path, err := Paths()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	parts := bytes.Split(data, []byte("\n"))
	if lines > 0 && len(parts) > lines+1 {
		parts = parts[len(parts)-lines-1:]
	}
	return string(bytes.Join(parts, []byte("\n"))), nil
}

func recordRuntimeError(runtimeErr error) error {
	store, err := orchestrator.NewStore()
	if err != nil {
		return err
	}
	return store.Update(func(st *orchestrator.State) error {
		if runtimeErr == nil {
			st.LastRuntimeError = ""
		} else {
			st.LastRuntimeError = runtimeErr.Error()
		}
		return nil
	})
}

func parsePID(value string) int { n, _ := strconv.Atoi(value); return n }

func storedAttachments(items []imessage.Attachment) []orchestrator.MessageAttachment {
	if len(items) == 0 {
		return nil
	}
	out := make([]orchestrator.MessageAttachment, len(items))
	for i, item := range items {
		out[i] = orchestrator.MessageAttachment{Path: item.Path, MimeType: item.MimeType, Name: item.Name}
	}
	return out
}

func messageAttachments(items []orchestrator.MessageAttachment) []imessage.Attachment {
	if len(items) == 0 {
		return nil
	}
	out := make([]imessage.Attachment, len(items))
	for i, item := range items {
		out[i] = imessage.Attachment{Path: item.Path, MimeType: item.MimeType, Name: item.Name}
	}
	return out
}
