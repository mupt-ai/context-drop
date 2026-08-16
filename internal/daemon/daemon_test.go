package daemon

import (
	"context"
	"errors"
	"fmt"
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

type fakeRuntime struct {
	mu       sync.Mutex
	launches []string
	owners   [][2]string
	err      error
}

func (f *fakeRuntime) Tasks(context.Context) ([]runtimeclient.ManagedTask, error) { return nil, nil }

func (f *fakeRuntime) LaunchManagedSchedule(_ context.Context, _, _, _, name, _, routerID, chatID string) (runtimeclient.ManagedTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.launches = append(f.launches, name)
	f.owners = append(f.owners, [2]string{routerID, chatID})
	return runtimeclient.ManagedTask{RunID: "run_test", PaneID: "w1:p2", Status: "running", FullyManaged: true}, f.err
}

type fakeNotifier struct {
	mu       sync.Mutex
	messages []string
}

func (f *fakeNotifier) Notify(title, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, title+":"+message)
	return nil
}

func TestResponderFailureReplyExplainsTimeout(t *testing.T) {
	reply := responderFailureReply(fmt.Errorf("Pi RPC responder: %w", context.DeadlineExceeded))
	if !strings.Contains(reply, "responder time limit") || !strings.Contains(reply, "delegate") {
		t.Fatalf("reply = %q", reply)
	}
}

func TestResponderFailureReplyDoesNotExposeInternalError(t *testing.T) {
	reply := responderFailureReply(errors.New("secret internal detail"))
	if strings.Contains(reply, "secret internal detail") {
		t.Fatalf("reply exposed internal error: %q", reply)
	}
}

func TestRunnerClaimsDueBeforeLaunchAndRecordsJob(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	store := orchestrator.Store{Path: filepath.Join(t.TempDir(), "state.json")}
	repo := t.TempDir()
	if err := store.Update(func(st *orchestrator.State) error {
		return orchestrator.Upsert(st, orchestrator.Schedule{Name: "check", Agent: "mock", Repo: repo, Prompt: "check", Every: time.Minute, Enabled: true}, now.Add(-time.Minute))
	}); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{}
	messageConfig := imessage.Defaults()
	messageConfig.Enabled, messageConfig.RouterMode, messageConfig.ChatID = true, true, "chat"
	r := &Runner{Store: store, Runtime: runtime, Notifier: &fakeNotifier{}, Now: func() time.Time { return now }, IMessage: &imessage.Adapter{Config: messageConfig}}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runtime.launches) != 1 || !reflect.DeepEqual(runtime.owners, [][2]string{{scheduleRouterID, "chat"}}) {
		t.Fatalf("launches=%#v owners=%#v", runtime.launches, runtime.owners)
	}
	st, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Jobs) != 1 || st.Jobs[0].Status != "completed" {
		t.Fatalf("jobs = %#v", st.Jobs)
	}
}

func TestRenderServiceEscapesPaths(t *testing.T) {
	plist := RenderLaunchAgent(`/tmp/a&<b`, `/tmp/log&<`, `/opt/homebrew/bin/node`, false)
	if strings.Contains(plist, `/tmp/a&<b`) || strings.Contains(plist, `/tmp/log&<`) {
		t.Fatalf("unescaped plist: %s", plist)
	}
	if !strings.Contains(plist, `/tmp/a&amp;&lt;b`) || !strings.Contains(plist, `/tmp/log&amp;&lt;`) {
		t.Fatalf("escaped paths missing: %s", plist)
	}
	unit := RenderSystemd(`/tmp/with space/context-drop`, `/opt/homebrew/bin/node`)
	if !strings.Contains(unit, `/tmp/with\x20space/context-drop`) || !strings.Contains(unit, `Restart=on-failure`) || !strings.Contains(unit, `Environment="PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"`) {
		t.Fatalf("bad unit: %s", unit)
	}
}
