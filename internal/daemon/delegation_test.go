package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/imessage"
	"contextdrop.dev/context-drop/internal/orchestrator"
	"contextdrop.dev/context-drop/internal/runtimeclient"
)

type fakeDelegationRuntime struct {
	mu              sync.Mutex
	healthFailures  int
	issued          int
	reports         []runtimeclient.ParentReport
	leased          map[string]bool
	abandoned       map[string]bool
	finishDelivered []bool
	finishedOwners  [][2]string
	confirmed       string
	confirmOwner    string
	confirmCalls    []string
	autoOutcome     string
	autoErr         error
	autoCalls       int
	finishFailures  int
	activeTask      runtimeclient.ManagedTask
	delegated       []string
	continued       []string
	registered      []map[string]string
	registeredOwner [2]string
	registeredID    string
	registerErr     error
	delegatedThread string
}

func (f *fakeDelegationRuntime) Health(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.healthFailures > 0 {
		f.healthFailures--
		return errors.New("not ready")
	}
	return nil
}
func (f *fakeDelegationRuntime) IssueRouterCapability(context.Context, string, string) (string, error) {
	f.issued++
	return "cap", nil
}
func (f *fakeDelegationRuntime) Delegate(_ context.Context, _ string, prompt, _ string) (runtimeclient.ManagedTask, error) {
	f.delegated = append(f.delegated, prompt)
	return runtimeclient.ManagedTask{PaneID: "worker-1", Status: "running", FullyManaged: true}, nil
}
func (f *fakeDelegationRuntime) DelegateInThread(ctx context.Context, capability, prompt, name, threadID string) (runtimeclient.ManagedTask, error) {
	f.delegatedThread = threadID
	return f.Delegate(ctx, capability, prompt, name)
}
func (f *fakeDelegationRuntime) RegisterIMessageThread(_ context.Context, routerID, chatID string, message map[string]string) (string, error) {
	f.registeredOwner = [2]string{routerID, chatID}
	f.registered = append(f.registered, message)
	if f.registerErr != nil {
		return "", f.registerErr
	}
	if f.registeredID == "" {
		f.registeredID = "thread-test"
	}
	return f.registeredID, nil
}
func (f *fakeDelegationRuntime) ActiveTask(context.Context, string) (runtimeclient.ManagedTask, bool, error) {
	return f.activeTask, f.activeTask.PaneID != "", nil
}
func (f *fakeDelegationRuntime) ContinueTask(_ context.Context, _ string, _ string, prompt string) (runtimeclient.ManagedTask, error) {
	f.continued = append(f.continued, prompt)
	return f.activeTask, nil
}
func (f *fakeDelegationRuntime) LeaseReport(_ context.Context, router, chat string) (runtimeclient.ParentReport, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.leased == nil {
		f.leased = map[string]bool{}
	}
	for _, r := range f.reports {
		if r.RouterID == router && r.ChatID == chat && !f.leased[r.ID] && !f.abandoned[r.ID] {
			f.leased[r.ID] = true
			r.LeaseID = "lease"
			return r, true, nil
		}
	}
	return runtimeclient.ParentReport{}, false, nil
}
func (f *fakeDelegationRuntime) FinishReport(_ context.Context, report runtimeclient.ParentReport, routerID, chatID string, delivered bool) error {
	return f.FinishReportWithError(context.Background(), report, routerID, chatID, delivered, "")
}
func (f *fakeDelegationRuntime) FinishReportWithError(_ context.Context, report runtimeclient.ParentReport, routerID, chatID string, delivered bool, errorClass string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finishDelivered = append(f.finishDelivered, delivered)
	f.finishedOwners = append(f.finishedOwners, [2]string{routerID, chatID})
	if f.finishFailures > 0 {
		f.finishFailures--
		return errors.New("finish failed")
	}
	if !delivered {
		if errorClass == "ambiguous" || errorClass == "permanent" {
			if f.abandoned == nil {
				f.abandoned = map[string]bool{}
			}
			f.abandoned[report.ID] = true
		} else {
			delete(f.leased, report.ID)
		}
	}
	return nil
}
func (f *fakeDelegationRuntime) AutoAuthorize(_ context.Context, _ runtimeclient.ParentReport, _, _ string) (runtimeclient.Run, string, error) {
	f.autoCalls++
	if f.autoErr != nil {
		return runtimeclient.Run{}, "", f.autoErr
	}
	outcome := f.autoOutcome
	if outcome == "" {
		outcome = "running"
	}
	return runtimeclient.Run{ID: "run_yolo"}, outcome, nil
}
func (f *fakeDelegationRuntime) Confirm(_ context.Context, router, _, token string) (runtimeclient.Run, error) {
	f.confirmCalls = append(f.confirmCalls, router)
	f.confirmed = token
	if token != "ABC123" || (f.confirmOwner != "" && router != f.confirmOwner) {
		return runtimeclient.Run{}, errors.New("invalid")
	}
	return runtimeclient.Run{ID: "run_authorized"}, nil
}

type reportCommander struct {
	mu    sync.Mutex
	sends []string
	fail  int
}

func (c *reportCommander) Run(_ context.Context, _ string, args []string, _ int) (imessage.CommandResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(args) > 0 && args[0] == "send" {
		if c.fail > 0 {
			c.fail--
			return imessage.CommandResult{}, errors.New("send failed")
		}
		c.sends = append(c.sends, args[4])
	}
	return imessage.CommandResult{Stdout: []byte(`{"ok":true}`)}, nil
}

func TestSafeDeliveryErrorKeepsUsefulCauseAndRedactsSecrets(t *testing.T) {
	if got := safeDeliveryError(fmt.Errorf("dial unix: connection refused")); strings.Contains(got, "connection refused") {
		t.Fatalf("unknown error type leaked detail: %q", got)
	}
	if got := safeDeliveryError(context.DeadlineExceeded); !strings.Contains(got, "deadline exceeded") {
		t.Fatalf("known-safe context error lost: %q", got)
	}
	secret := "sk-abc123xyz with no keyword that matches the old blacklist"
	if got := safeDeliveryError(fmt.Errorf("send failed: %s", secret)); strings.Contains(got, secret) || strings.Contains(got, "abc123") {
		t.Fatalf("diagnostic leaked secret-looking value: %q", got)
	}
}

func TestClassifyDeliveryError(t *testing.T) {
	tests := []struct {
		name       string
		respondErr error
		sendErr    error
		want       string
	}{
		{name: "success", want: ""},
		{name: "responder timeout", respondErr: context.DeadlineExceeded, sendErr: context.DeadlineExceeded, want: "timeout"},
		{name: "send timeout is ambiguous", sendErr: context.DeadlineExceeded, want: "ambiguous"},
		{name: "client error after send is ambiguous", sendErr: &runtimeclient.HTTPError{StatusCode: http.StatusBadRequest}, want: "ambiguous"},
		{name: "request timeout after send is ambiguous", sendErr: &runtimeclient.HTTPError{StatusCode: http.StatusRequestTimeout}, want: "ambiguous"},
		{name: "rate limit after send is ambiguous", sendErr: &runtimeclient.HTTPError{StatusCode: http.StatusTooManyRequests}, want: "ambiguous"},
		{name: "server error after send is ambiguous", sendErr: &runtimeclient.HTTPError{StatusCode: http.StatusBadGateway}, want: "ambiguous"},
		{name: "unknown send failure is ambiguous", sendErr: errors.New("network unavailable"), want: "ambiguous"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyDeliveryError(tt.respondErr, tt.sendErr); got != tt.want {
				t.Fatalf("classifyDeliveryError()=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestReportDeliveryDoesNotRetryAfterAmbiguousSendFailureAndScopesChat(t *testing.T) {
	backend := &fakeDelegationRuntime{reports: []runtimeclient.ParentReport{{ID: "r-other", RouterID: imessageRouterID, ChatID: "other", RunID: "x", Kind: "completed", Message: "secret"}, {ID: "r1", RouterID: imessageRouterID, ChatID: "chat", RunID: "run_123", Kind: "failed", Message: "bad\n\x1b[31m\u202Ething"}}}
	commander := &reportCommander{fail: 1}
	cfg := imessage.Defaults()
	cfg.Enabled = true
	cfg.RouterMode = true
	cfg.ChatID = "chat"
	cfg.ImsgPath = "/bin/echo"
	responder := &recordingResponder{}
	runner := &Runner{Delegation: backend, IMessage: &imessage.Adapter{Config: cfg, Commander: commander, PersistentResponder: responder}}
	runner.deliverReportsOnce(context.Background())
	runner.deliverReportsOnce(context.Background())
	if len(commander.sends) != 0 || len(responder.prompts) != 1 || strings.Contains(responder.prompts[0], "\x1b") || strings.Contains(responder.prompts[0], "\u202E") {
		t.Fatalf("sends=%q prompts=%q", commander.sends, responder.prompts)
	}
	if !reflect.DeepEqual(backend.finishDelivered, []bool{false}) {
		t.Fatalf("finishes=%v", backend.finishDelivered)
	}
}

func TestScheduleOwnedReportUsesConversationalResponse(t *testing.T) {
	backend := &fakeDelegationRuntime{reports: []runtimeclient.ParentReport{{ID: "schedule-report", RouterID: scheduleRouterID, ChatID: "chat", RunID: "run", Message: "scheduled work finished"}}}
	commander := &reportCommander{}
	cfg := imessage.Defaults()
	cfg.Enabled, cfg.RouterMode, cfg.ChatID, cfg.ImsgPath = true, true, "chat", "/bin/echo"
	responder := &recordingResponder{}
	runner := &Runner{Delegation: backend, IMessage: &imessage.Adapter{Config: cfg, Commander: commander, PersistentResponder: responder}}
	runner.deliverReportsOnce(context.Background())
	if (len(responder.prompts) != 1 || !strings.Contains(responder.prompts[0], "scheduled work finished")) || !reflect.DeepEqual(backend.finishedOwners, [][2]string{{scheduleRouterID, "chat"}}) || !reflect.DeepEqual(commander.sends, []string{"router reply"}) {
		t.Fatalf("prompts=%v owners=%v sends=%v", responder.prompts, backend.finishedOwners, commander.sends)
	}
}

func TestScheduleOwnedReportRecordsVerifiedDelivery(t *testing.T) {
	now := time.Now().UTC()
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	schedule := orchestrator.Schedule{Name: "meal", Type: orchestrator.ScheduleAgent, Agent: "mock", Repo: t.TempDir(), Prompt: "ask", Every: time.Hour, Enabled: true}
	job := orchestrator.NewJobWithOccurrence(schedule, "running", "occurrence", now)
	job.RuntimeRunID = "run-scheduled"
	if err := store.Update(func(st *orchestrator.State) error { st.Jobs = append(st.Jobs, job); return nil }); err != nil {
		t.Fatal(err)
	}
	backend := &fakeDelegationRuntime{reports: []runtimeclient.ParentReport{{ID: "report-user", RouterID: scheduleRouterID, ChatID: "chat", RunID: "run-scheduled", Message: "What did you eat and when?"}}}
	commander := &reportCommander{}
	cfg := imessage.Defaults()
	cfg.Enabled, cfg.RouterMode, cfg.ChatID, cfg.ImsgPath = true, true, "chat", "/bin/echo"
	runner := &Runner{Store: store, Now: func() time.Time { return now }, Delegation: backend, IMessage: &imessage.Adapter{Config: cfg, Commander: commander, PersistentResponder: &recordingResponder{}}}
	runner.deliverReportsOnce(context.Background())
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	got := state.Jobs[0]
	if got.DeliveryStatus != "delivered" || got.DeliveryReportID != "report-user" || got.DeliveredAt == nil || !reflect.DeepEqual(commander.sends, []string{"router reply"}) {
		t.Fatalf("job=%#v sends=%v", got, commander.sends)
	}
}

func TestDeliveredScheduleReportRetriesOnlyAckAfterReceipt(t *testing.T) {
	now := time.Now().UTC()
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	schedule := orchestrator.Schedule{Name: "meal", Type: orchestrator.ScheduleAgent, Agent: "mock", Repo: t.TempDir(), Prompt: "ask", Every: time.Hour, Enabled: true}
	job := orchestrator.NewJobWithOccurrence(schedule, "running", "occurrence", now)
	job.RuntimeRunID = "run-scheduled"
	if err := store.Update(func(st *orchestrator.State) error { st.Jobs = append(st.Jobs, job); return nil }); err != nil {
		t.Fatal(err)
	}
	backend := &fakeDelegationRuntime{finishFailures: 1, reports: []runtimeclient.ParentReport{{ID: "report-user", RouterID: scheduleRouterID, ChatID: "chat", RunID: "run-scheduled", Message: "What did you eat and when?"}}}
	commander := &reportCommander{}
	cfg := imessage.Defaults()
	cfg.Enabled, cfg.RouterMode, cfg.ChatID, cfg.ImsgPath = true, true, "chat", "/bin/echo"
	runner := &Runner{Store: store, Now: func() time.Time { return now }, Delegation: backend, IMessage: &imessage.Adapter{Config: cfg, Commander: commander, PersistentResponder: &recordingResponder{}}}
	runner.deliverReportsOnce(context.Background())
	backend.leased = map[string]bool{}
	runner.deliverReportsOnce(context.Background())
	if !reflect.DeepEqual(commander.sends, []string{"router reply"}) || !reflect.DeepEqual(backend.finishDelivered, []bool{true, true}) {
		t.Fatalf("sends=%v finishes=%v", commander.sends, backend.finishDelivered)
	}
}

func TestWorkerQuestionFallbackIsNatural(t *testing.T) {
	backend := &fakeDelegationRuntime{reports: []runtimeclient.ParentReport{{ID: "question", RouterID: imessageRouterID, ChatID: "chat", RunID: "run", Worker: 1, Kind: "needs_user", Message: "did you eat breakfast today?"}}}
	commander := &reportCommander{}
	cfg := imessage.Defaults()
	cfg.Enabled, cfg.RouterMode, cfg.ChatID, cfg.ImsgPath = true, true, "chat", "/bin/echo"
	responder := &recordingResponder{response: imessage.Response{ToolCompleted: true}}
	runner := &Runner{Now: time.Now, Delegation: backend, IMessage: &imessage.Adapter{Config: cfg, Commander: commander, PersistentResponder: responder}}
	runner.deliverReportsOnce(context.Background())
	if !reflect.DeepEqual(commander.sends, []string{"did you eat breakfast today?"}) {
		t.Fatalf("question was wrapped or lost: %v", commander.sends)
	}
}

func TestSilentScheduleReports(t *testing.T) {
	for _, kind := range []string{"progress", "completed", "needs_user", "failed"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Now().UTC()
			store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
			schedule := orchestrator.Schedule{Name: "backend", Silent: true}
			job := orchestrator.NewJobWithOccurrence(schedule, "running", "run", now)
			job.RuntimeRunID = "run"
			if err := store.Update(func(st *orchestrator.State) error {
				st.Schedules = append(st.Schedules, schedule)
				st.Jobs = append(st.Jobs, job)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			backend := &fakeDelegationRuntime{reports: []runtimeclient.ParentReport{{ID: "report", RouterID: scheduleRouterID, ChatID: "chat", RunID: "run", Kind: kind, Message: "result"}}}
			commander := &reportCommander{}
			cfg := imessage.Defaults()
			cfg.Enabled, cfg.RouterMode, cfg.ChatID, cfg.ImsgPath = true, true, "chat", "/bin/echo"
			responder := &recordingResponder{}
			runner := &Runner{Store: store, Now: func() time.Time { return now }, Delegation: backend, IMessage: &imessage.Adapter{Config: cfg, Commander: commander, PersistentResponder: responder}}
			runner.deliverReportsOnce(context.Background())
			state, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			visible := kind == "needs_user" || kind == "failed"
			if (len(responder.prompts) != 0) != visible || (len(commander.sends) != 0) != visible || state.ReportDeliveries["report"].UserVisible != visible {
				t.Fatalf("prompts=%v sends=%v receipt=%+v", responder.prompts, commander.sends, state.ReportDeliveries["report"])
			}
			if !reflect.DeepEqual(backend.finishDelivered, []bool{true}) {
				t.Fatalf("report not acknowledged: %v", backend.finishDelivered)
			}
			if kind == "completed" && (state.Jobs[0].Status != "completed" || state.Jobs[0].DeliveryStatus != "silent") {
				t.Fatalf("silent completion not recorded: %+v", state.Jobs[0])
			}
		})
	}
}

func TestScheduleFinalReportCompletesJobAndReachesMain(t *testing.T) {
	now := time.Now().UTC()
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	schedule := orchestrator.Schedule{Name: "nightly", Type: orchestrator.ScheduleAgent, Backend: "herdr", Agent: "mock", Repo: t.TempDir(), Prompt: "work", Every: time.Hour, Enabled: true}
	// The scheduler may conservatively mark the job unknown if it observes the
	// pane between the useful report ACK and the lifecycle report delivery.
	job := orchestrator.NewJobWithOccurrence(schedule, "unknown", "run-scheduled", now)
	job.RuntimeRunID = "run-scheduled"
	if err := store.Update(func(st *orchestrator.State) error { st.Jobs = append(st.Jobs, job); return nil }); err != nil {
		t.Fatal(err)
	}
	backend := &fakeDelegationRuntime{reports: []runtimeclient.ParentReport{{ID: "schedule-lifecycle", RouterID: scheduleRouterID, ChatID: "chat", RunID: "run-scheduled", Worker: 2, Kind: "completed", Message: "done"}}}
	commander := &reportCommander{}
	cfg := imessage.Defaults()
	cfg.Enabled, cfg.RouterMode, cfg.ChatID, cfg.ImsgPath = true, true, "chat", "/bin/echo"
	responder := &recordingResponder{}
	runner := &Runner{Store: store, Now: func() time.Time { return now }, Delegation: backend, IMessage: &imessage.Adapter{Config: cfg, Commander: commander, PersistentResponder: responder}}
	runner.deliverReportsOnce(context.Background())
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Jobs[0].Status != "completed" || len(responder.prompts) != 1 || len(commander.sends) != 1 || !reflect.DeepEqual(backend.finishDelivered, []bool{true}) {
		t.Fatalf("job=%#v prompts=%v sends=%v finishes=%v", state.Jobs[0], responder.prompts, commander.sends, backend.finishDelivered)
	}
}

func TestScheduleFailureLifecycleMarksFailureAndSendsOneNotice(t *testing.T) {
	now := time.Now().UTC()
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	schedule := orchestrator.Schedule{Name: "meal", Type: orchestrator.ScheduleAgent, Agent: "mock", Repo: t.TempDir(), Prompt: "ask", Every: time.Hour, Enabled: true}
	job := orchestrator.NewJobWithOccurrence(schedule, "running", "occurrence", now)
	job.RuntimeRunID = "run-scheduled"
	if err := store.Update(func(st *orchestrator.State) error {
		st.Schedules = append(st.Schedules, schedule)
		st.Jobs = append(st.Jobs, job)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	backend := &fakeDelegationRuntime{reports: []runtimeclient.ParentReport{{ID: "failure", RouterID: scheduleRouterID, ChatID: "chat", RunID: "run-scheduled", Message: "The worker pane closed without sending a final report.", Worker: 2, Kind: "failed"}}}
	commander := &reportCommander{}
	cfg := imessage.Defaults()
	cfg.Enabled, cfg.RouterMode, cfg.ChatID, cfg.ImsgPath = true, true, "chat", "/bin/echo"
	runner := &Runner{Store: store, Now: func() time.Time { return now }, Delegation: backend, IMessage: &imessage.Adapter{Config: cfg, Commander: commander, PersistentResponder: &recordingResponder{}}}
	runner.deliverReportsOnce(context.Background())
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Jobs[0].Status != "failed" || state.Jobs[0].DeliveryStatus != "failure_notice_delivered" || state.Schedules[0].ConsecutiveFailures != 1 || len(commander.sends) != 1 || commander.sends[0] != "router reply" {
		t.Fatalf("state=%#v sends=%v", state, commander.sends)
	}
}

func TestReportDeliveryUsesHTTPLeaseAndAbandonsAmbiguousSend(t *testing.T) {
	var mu sync.Mutex
	leased, delivered, abandoned, releases, acks := false, false, false, 0, 0
	var releaseErrorClass string
	var leaseSeconds int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if req.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch req.URL.Path {
		case "/v1/reports/lease":
			var input struct {
				LeaseSeconds int `json:"leaseSeconds"`
			}
			_ = json.NewDecoder(req.Body).Decode(&input)
			leaseSeconds = input.LeaseSeconds
			if delivered || abandoned || leased {
				_ = json.NewEncoder(w).Encode(map[string]any{})
				return
			}
			leased = true
			_ = json.NewEncoder(w).Encode(map[string]any{"report": runtimeclient.ParentReport{ID: "r1", RunID: "run", RouterID: imessageRouterID, ChatID: "chat", Kind: "completed", Message: "done", LeaseID: "lease"}})
		case "/v1/reports/r1/release":
			var input struct {
				ErrorClass string `json:"errorClass"`
			}
			_ = json.NewDecoder(req.Body).Decode(&input)
			releaseErrorClass = input.ErrorClass
			abandoned = input.ErrorClass == "ambiguous" || input.ErrorClass == "permanent"
			leased = false
			releases++
			_ = json.NewEncoder(w).Encode(map[string]any{"report": map[string]any{"id": "r1"}})
		case "/v1/reports/r1/ack":
			delivered = true
			leased = false
			acks++
			_ = json.NewEncoder(w).Encode(map[string]any{"report": map[string]any{"id": "r1"}})
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	client := &runtimeclient.Client{Address: server.URL, Token: "secret", HTTP: server.Client()}
	commander := &reportCommander{fail: 1}
	cfg := imessage.Defaults()
	cfg.Enabled = true
	cfg.RouterMode = true
	cfg.ChatID = "chat"
	cfg.ImsgPath = "/bin/echo"
	runner := &Runner{Delegation: client, IMessage: &imessage.Adapter{Config: cfg, Commander: commander, PersistentResponder: &recordingResponder{}}}
	runner.deliverReportsOnce(context.Background())
	runner.deliverReportsOnce(context.Background())
	if releases != 1 || acks != 0 || len(commander.sends) != 0 || releaseErrorClass != "ambiguous" || leaseSeconds < int(imessage.MaxTrustedResponderDuration/time.Second)+cfg.SendTimeoutSeconds {
		t.Fatalf("releases=%d acks=%d errorClass=%q leaseSeconds=%d sends=%v", releases, acks, releaseErrorClass, leaseSeconds, commander.sends)
	}
}

func TestEveryPlainReportGetsAnUntrustedOrchestratorTurn(t *testing.T) {
	backend := &fakeDelegationRuntime{reports: []runtimeclient.ParentReport{{ID: "r1", RouterID: imessageRouterID, ChatID: "chat", RunID: "run", Kind: "progress", Message: "ordinary progress", ThreadID: "thread-opaque"}}}
	commander := &reportCommander{}
	cfg := imessage.Defaults()
	cfg.Enabled, cfg.RouterMode, cfg.ChatID, cfg.ImsgPath = true, true, "chat", "/bin/echo"
	responder := &recordingResponder{reply: "orchestrator response"}
	runner := &Runner{Delegation: backend, IMessage: &imessage.Adapter{Config: cfg, Commander: commander, PersistentResponder: responder}}
	runner.deliverReportsOnce(context.Background())
	if len(responder.prompts) != 1 || len(commander.sends) != 1 || commander.sends[0] != "orchestrator response" || len(backend.finishDelivered) != 1 || !backend.finishDelivered[0] {
		t.Fatalf("prompts=%v sends=%v finishes=%v", responder.prompts, commander.sends, backend.finishDelivered)
	}
	prompt := responder.prompts[0]
	if !strings.Contains(prompt, "ordinary progress") || !strings.Contains(prompt, "not a user instruction") || strings.Contains(prompt, "Available task tools remain enabled") {
		t.Fatalf("plain report did not reach an ordinary orchestrator turn: %q", prompt)
	}
}

func TestThreadReplyReportSideEffectIsAcknowledgedWithoutDuplicateSend(t *testing.T) {
	backend := &fakeDelegationRuntime{reports: []runtimeclient.ParentReport{{ID: "r1", RouterID: imessageRouterID, ChatID: "chat", RunID: "run", Message: "done", ThreadID: "thread-opaque"}}}
	commander := &reportCommander{}
	cfg := imessage.Defaults()
	cfg.Enabled, cfg.RouterMode, cfg.ChatID, cfg.ImsgPath = true, true, "chat", "/bin/echo"
	responder := &recordingResponder{fail: 1, response: imessage.Response{ToolCompleted: true, SideEffectToolCompleted: true, MessagingSideEffectToolCompleted: true, ThreadReplyToolCompleted: true}}
	runner := &Runner{Delegation: backend, IMessage: &imessage.Adapter{Config: cfg, Commander: commander, PersistentResponder: responder}}
	runner.deliverReportsOnce(context.Background())
	if len(commander.sends) != 0 || !reflect.DeepEqual(backend.finishDelivered, []bool{true}) {
		t.Fatalf("sends=%v finishes=%v", commander.sends, backend.finishDelivered)
	}
}

func TestReportOrchestratorFailureReleasesWithoutSending(t *testing.T) {
	backend := &fakeDelegationRuntime{reports: []runtimeclient.ParentReport{{ID: "r1", RouterID: imessageRouterID, ChatID: "chat", RunID: "run", Kind: "completed", Message: "secret worker body"}}}
	var logs bytes.Buffer
	originalOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(originalOutput) })
	commander := &reportCommander{}
	cfg := imessage.Defaults()
	cfg.Enabled = true
	cfg.RouterMode = true
	cfg.ChatID = "chat"
	cfg.ImsgPath = "/bin/echo"
	runner := &Runner{Delegation: backend, IMessage: &imessage.Adapter{Config: cfg, Commander: commander, PersistentResponder: &recordingResponder{fail: 1}}}
	runner.deliverReportsOnce(context.Background())
	if len(commander.sends) != 0 || len(backend.finishDelivered) != 1 || backend.finishDelivered[0] {
		t.Fatalf("sends=%v finishes=%v", commander.sends, backend.finishDelivered)
	}
	if !strings.Contains(logs.String(), "report r1 orchestrator turn failed") || strings.Contains(logs.String(), "secret worker body") {
		t.Fatalf("unsafe or missing orchestrator failure log: %q", logs.String())
	}
}

type recordingResponder struct {
	prompts  []string
	fail     int
	reply    string
	response imessage.Response
}

func (*recordingResponder) Prepare(context.Context) (imessage.PersistentResponderState, error) {
	return imessage.PersistentResponderState{}, nil
}
func (r *recordingResponder) Respond(_ context.Context, p string, _ int) (imessage.Response, error) {
	r.prompts = append(r.prompts, p)
	if r.fail > 0 {
		r.fail--
		return r.response, errors.New("orchestrator failed")
	}
	if r.response.Reply != "" || r.response.ToolCompleted {
		return r.response, nil
	}
	reply := r.reply
	if reply == "" {
		reply = "router reply"
	}
	return imessage.Response{Reply: reply}, nil
}
func (*recordingResponder) Close() error { return nil }
func TestActiveTaskDoesNotInterceptCasualRouterMessage(t *testing.T) {
	commander := &reportCommander{}
	responder := &recordingResponder{}
	cfg := imessage.Defaults()
	cfg.Enabled = true
	cfg.Trusted = true
	cfg.RouterMode = true
	cfg.ChatID = "chat"
	cfg.ImsgPath = "/bin/echo"
	now := time.Now().UTC()
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	_ = store.Update(func(st *orchestrator.State) error {
		st.MessageJobs["1"] = orchestrator.MessageJob{MessageID: "1", ClaimedAt: now}
		return nil
	})
	runner := &Runner{Store: store, Now: func() time.Time { return now }, Delegation: &fakeDelegationRuntime{}, IMessage: &imessage.Adapter{Config: cfg, Commander: commander, PersistentResponder: responder}}
	runner.processMessage(context.Background(), imessage.Message{ID: "1", ChatID: "chat", Text: "thanks"})
	if len(responder.prompts) != 1 {
		t.Fatalf("router prompts=%d", len(responder.prompts))
	}
}

func TestConfigureRouterHealthGatesAndRotatesOverHTTP(t *testing.T) {
	t.Setenv("CONTEXT_DROP_HOME", t.TempDir())
	cfg := imessage.Defaults()
	cfg.Trusted = true
	cfg.RouterMode = true
	cfg.ChatID = "chat-a"
	cfg.ResponderCwd = t.TempDir()
	session := filepath.Join(t.TempDir(), "original-session.jsonl")
	if err := os.WriteFile(session, []byte(fmt.Sprintf("{\"type\":\"session\",\"cwd\":%q}\n", cfg.ResponderCwd)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.ResponderCommand = []string{"/tmp/pi", "--session", session, "@{prompt_file}"}
	responder, ok, err := imessage.NewPiRPCResponder(cfg)
	if err != nil || !ok {
		t.Fatalf("responder ok=%v err=%v", ok, err)
	}
	var mu sync.Mutex
	healthCalls := 0
	issued := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch req.URL.Path {
		case "/health":
			healthCalls++
			if healthCalls == 1 {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		case "/v1/router-capabilities":
			issued++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"capability": fmt.Sprintf("cap-%d", issued)})
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	client := &runtimeclient.Client{Address: server.URL, Token: "secret", HTTP: server.Client()}
	runner := &Runner{Delegation: client, IMessage: &imessage.Adapter{Config: cfg, PersistentResponder: responder}}
	if err := runner.configureRouterWithRetry(context.Background(), 3, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	url, first := responder.DelegationEnv()
	if !strings.HasSuffix(url, "/v1/tasks/delegate") || first != "cap-1" {
		t.Fatalf("url=%q cap=%q", url, first)
	}
	if err := runner.configureRouter(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, second := responder.DelegationEnv()
	if second != "cap-2" || second == first || runner.routerToken() != second {
		t.Fatalf("first=%q second=%q runner=%q", first, second, runner.routerToken())
	}
}
