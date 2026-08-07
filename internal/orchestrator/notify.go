package orchestrator

import (
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"strings"
)

type Notifier interface {
	Notify(title, message string) error
}

type LocalNotifier struct {
	GOOS string
	Run  func(name string, args ...string) ([]byte, error)
}

func (n LocalNotifier) Notify(title, message string) error {
	goos := n.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "darwin" {
		log.Printf("Context Drop notification: %s: %s", title, message)
		return nil
	}
	// Values are passed as argv, not interpolated into AppleScript source.
	script := `on run argv
set notificationTitle to item 1 of argv
set notificationMessage to item 2 of argv
display notification notificationMessage with title notificationTitle
end run`
	run := n.Run
	if run == nil {
		run = func(name string, args ...string) ([]byte, error) { return exec.Command(name, args...).CombinedOutput() }
	}
	output, err := run("osascript", "-e", script, title, message)
	if err != nil {
		return fmt.Errorf("local notification failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
