package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"contextdrop.dev/context-drop/internal/cli"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	exit    = os.Exit
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := cli.NewRootCommand(cli.BuildInfo{Version: version, Commit: commit, Date: date})
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetContext(ctx)
	return cmd.Execute()
}
