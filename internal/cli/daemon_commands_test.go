package cli

import "testing"

func TestDaemonAndScheduleCommandsRegistered(t *testing.T) {
	root := NewRootCommand(BuildInfo{})
	for _, path := range [][]string{{"daemon", "status"}, {"daemon", "install"}, {"daemon", "watchdog", "status"}, {"schedule", "add"}, {"schedule", "run"}} {
		command, _, err := root.Find(path)
		if err != nil || command == root {
			t.Fatalf("command %v not registered: %v", path, err)
		}
	}
}
