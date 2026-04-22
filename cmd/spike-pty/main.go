// spike-pty: minimal reproducer for embedding `claude --resume <id>` inside
// a pseudo-terminal. No emulator, no Bubble Tea — straight PTY passthrough.
// If this renders Claude cleanly, the PTY half of the plan is sound.
//
// Usage: go run ./cmd/spike-pty <session-id>
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func main() {
	arg := ""
	if len(os.Args) >= 2 {
		arg = os.Args[1]
	}
	if err := run(arg); err != nil {
		fmt.Fprintln(os.Stderr, "spike-pty:", err)
		os.Exit(1)
	}
}

func run(sessionID string) error {
	var cmd *exec.Cmd
	switch sessionID {
	case "":
		cmd = exec.Command("claude")
	case "pick":
		cmd = exec.Command("claude", "--resume")
	default:
		cmd = exec.Command("claude", "--resume", sessionID)
	}
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("pty start: %w", err)
	}
	defer ptmx.Close()

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			_ = pty.InheritSize(os.Stdin, ptmx)
		}
	}()
	ch <- syscall.SIGWINCH

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("makeraw: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	go func() { _, _ = io.Copy(ptmx, os.Stdin) }()
	_, _ = io.Copy(os.Stdout, ptmx)

	return cmd.Wait()
}
