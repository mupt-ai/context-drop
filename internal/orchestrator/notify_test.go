package orchestrator

import (
	"errors"
	"testing"
)

func TestNotifierPassesValuesAsArguments(t *testing.T) {
	var name string
	var args []string
	n := LocalNotifier{GOOS: "darwin", Run: func(got string, gotArgs ...string) ([]byte, error) { name, args = got, gotArgs; return nil, nil }}
	title := `title" & shell script "bad"`
	message := "message; rm -rf /"
	if err := n.Notify(title, message); err != nil {
		t.Fatal(err)
	}
	if name != "osascript" || len(args) != 4 || args[2] != title || args[3] != message {
		t.Fatalf("call = %s %#v", name, args)
	}
}

func TestNotifierReportsCommandFailure(t *testing.T) {
	n := LocalNotifier{GOOS: "darwin", Run: func(string, ...string) ([]byte, error) { return []byte("denied"), errors.New("exit") }}
	if err := n.Notify("title", "message"); err == nil {
		t.Fatal("expected error")
	}
}

func TestNotifierNonDarwinDoesNotInvokeCommand(t *testing.T) {
	n := LocalNotifier{GOOS: "linux", Run: func(string, ...string) ([]byte, error) { t.Fatal("command invoked"); return nil, nil }}
	if err := n.Notify("title", "message"); err != nil {
		t.Fatal(err)
	}
}
