package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/bduffany/kpf/internal/client"
	"github.com/bduffany/kpf/internal/daemon"
	"github.com/bduffany/kpf/internal/protocol"
)

// Main runs the kpf CLI entrypoint and exits non-zero on error.
func Main() {
	args := os.Args[1:]
	if client.IsHelpCommand(args) {
		if err := client.RunHelp(); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				os.Exit(exitErr.ExitCode())
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if len(args) > 0 && client.IsCompletionCommand(args[0]) {
		if err := client.RunCompletion(args); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				os.Exit(exitErr.ExitCode())
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	var err error
	if len(args) > 0 && args[0] == protocol.DaemonArg {
		err = daemon.Run()
	} else {
		err = client.Run(args)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
