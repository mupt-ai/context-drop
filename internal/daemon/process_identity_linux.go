//go:build linux

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type processIdentity struct {
	Executable string
	StartToken string
}

func inspectProcess(pid int) (processIdentity, error) {
	executable, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return processIdentity{}, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
		executable = resolved
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return processIdentity{}, err
	}
	// The command field is parenthesized and may contain spaces. Start time is
	// field 22, hence index 19 after the closing parenthesis.
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return processIdentity{}, fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(stat)[end+1:])
	if len(fields) <= 19 {
		return processIdentity{}, fmt.Errorf("invalid process stat fields")
	}
	return processIdentity{Executable: executable, StartToken: fields[19]}, nil
}
