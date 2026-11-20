package pool

import (
	"context"
	"errors"
	"testing"
)

type machineStub struct{ sent []LocalSend }

func (m *machineStub) Agents(context.Context) ([]Agent, error) {
	return []Agent{
		{ID: "laptop/cc-00000001", Cwd: "/work/app"},
		{ID: "laptop/cc-00000002", Cwd: "/work/app/web"},
		{ID: "laptop/cc-00000003", Cwd: "/work/apple"},
		{ID: "laptop/cc-00000004", Cwd: "/home/me/taxes"},
	}, nil
}

func (m *machineStub) Read(_ context.Context, in LocalRead) (ReadResult, error) {
	return ReadResult{Agent: Agent{ID: "laptop/" + in.ID}}, nil
}

func (m *machineStub) Ask(_ context.Context, in LocalAsk) (AskResult, error) {
	return AskResult{ID: in.ID, Answer: "answer"}, nil
}

func (m *machineStub) Send(_ context.Context, in LocalSend) (SendResult, error) {
	m.sent = append(m.sent, in)
	return SendResult{ID: "laptop/" + in.ID, Delivered: true}, nil
}

func errCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestSharedOnlyExposesSharedDirectories(t *testing.T) {
	local := &machineStub{}
	s := Shared{Local: local, Dirs: []string{"/work/app/"}}
	ctx := context.Background()

	list, _ := s.Agents(ctx)
	if len(list) != 2 || list[0].ID != "laptop/cc-00000001" || list[1].ID != "laptop/cc-00000002" {
		t.Fatalf("shared agents = %+v", list)
	}
	if _, err := s.Read(ctx, LocalRead{ID: "cc-00000002"}); err != nil {
		t.Fatalf("read a shared agent: %v", err)
	}
	// Outside the shared directory (and a sibling that only shares a prefix) is not found.
	for _, id := range []string{"cc-00000003", "cc-00000004"} {
		if _, err := s.Read(ctx, LocalRead{ID: id}); errCode(err) != "not_found" {
			t.Fatalf("read %s: %v", id, err)
		}
		if _, err := s.Send(ctx, LocalSend{ID: id, Text: "hi"}); errCode(err) != "not_found" {
			t.Fatalf("send %s: %v", id, err)
		}
	}
	if _, err := s.Send(ctx, LocalSend{ID: "cc-00000001", Text: "hi"}); err != nil || len(local.sent) != 1 {
		t.Fatalf("send a shared agent: %v, %v", err, local.sent)
	}
}

func TestSharedReadOnlyRefusesEverySend(t *testing.T) {
	local := &machineStub{}
	s := Shared{Local: local, All: true, ReadOnly: true}
	ctx := context.Background()
	if list, _ := s.Agents(ctx); len(list) != 4 {
		t.Fatalf("all agents = %+v", list)
	}
	if _, err := s.Read(ctx, LocalRead{ID: "cc-00000004"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(ctx, LocalSend{ID: "cc-00000001", Text: "rm -rf"}); errCode(err) != "forbidden" || len(local.sent) != 0 {
		t.Fatalf("read-only send: %v, %v", err, local.sent)
	}
}
