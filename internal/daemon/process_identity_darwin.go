//go:build darwin

package daemon

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type processIdentity struct {
	Executable string
	StartToken string
}

func inspectProcess(pid int) (processIdentity, error) {
	pidText := strconv.Itoa(pid)
	executableBytes, err := exec.Command("ps", "-p", pidText, "-o", "comm=").Output()
	if err != nil {
		return processIdentity{}, err
	}
	startBytes, err := exec.Command("ps", "-p", pidText, "-o", "lstart=").Output()
	if err != nil {
		return processIdentity{}, err
	}
	executable := strings.TrimSpace(string(executableBytes))
	if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
		executable = resolved
	}
	start := strings.Join(strings.Fields(string(startBytes)), " ")
	if executable == "" || start == "" {
		return processIdentity{}, fmt.Errorf("process identity unavailable")
	}
	return processIdentity{Executable: executable, StartToken: start}, nil
}
