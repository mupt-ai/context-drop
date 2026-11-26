package extensions

import (
	"context"
	"io"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"contextdrop.dev/context-drop/internal/pool"
)

// CLI is what the context-drop command gives an extension's subcommands.
type CLI struct {
	// Org is this machine's org membership, or an error saying how to join one.
	Org func() (pool.Membership, error)
	// Server is the org server this machine uses, for printing setup that names it.
	Server func() string
	// Agent is the calling agent's session on this machine, if an agent runs the command.
	Agent func(context.Context) string
	// JSON writes v as indented JSON.
	JSON func(io.Writer, any) error
}

// Client is the org client for this machine.
func (c CLI) Client() (pool.Client, pool.Membership, error) {
	m, err := c.Org()
	return pool.Client{Membership: m}, m, err
}

// Command is an extension's `context-drop NAME` command.
type Command func(CLI) *cobra.Command

// OpenBrowser opens a link in the person's default browser. The caller prints the link as well.
func OpenBrowser(link string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, link).Start()
}
