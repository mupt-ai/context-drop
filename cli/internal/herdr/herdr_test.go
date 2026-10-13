package herdr

import (
	"context"
	"reflect"
	"testing"
)

func TestDariLaunchCommands(t *testing.T) {
	var calls [][]string
	client := Client{Run: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		return nil, nil
	}}
	if err := client.RunCommand(context.Background(), "w1:p9", "dari claude"); err != nil {
		t.Fatal(err)
	}
	if err := client.Rename(context.Background(), "w1:p9", "task"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"pane", "run", "w1:p9", "dari claude"}, {"agent", "rename", "w1:p9", "task"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}
