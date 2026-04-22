// Package ptty wraps a creack/pty + vt10x emulator pair so callers can drive
// an interactive child process and render its screen inside another TUI
// without fighting the host terminal.
package ptty

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/ActiveState/vt10x"
	"github.com/creack/pty"
)

type Session struct {
	cmd   *exec.Cmd
	ptmx  *os.File
	state *vt10x.State
	vt    *vt10x.VT
	cols  int
	rows  int

	done chan struct{}
	once sync.Once
}

// New spawns cmd in a PTY sized cols×rows. Emulator parsing and child-wait
// run in background goroutines.
func New(cmd *exec.Cmd, cols, rows int) (*Session, error) {
	if cols <= 0 || rows <= 0 {
		cols, rows = 120, 40
	}
	winSize := &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}
	ptmx, err := pty.StartWithSize(cmd, winSize)
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}
	state := &vt10x.State{}
	vt, err := vt10x.Create(state, ptmx)
	if err != nil {
		_ = ptmx.Close()
		return nil, fmt.Errorf("vt create: %w", err)
	}
	vt.Resize(cols, rows)

	s := &Session{
		cmd:   cmd,
		ptmx:  ptmx,
		state: state,
		vt:    vt,
		cols:  cols,
		rows:  rows,
		done:  make(chan struct{}),
	}

	go func() { _ = vt.Parse() }()
	go func() {
		_ = cmd.Wait()
		s.once.Do(func() { close(s.done) })
	}()
	return s, nil
}

// Write forwards bytes (keystrokes) to the child's stdin.
func (s *Session) Write(p []byte) (int, error) {
	return s.ptmx.Write(p)
}

// Resize updates emulator grid + PTY window size (SIGWINCH to child).
func (s *Session) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	s.cols, s.rows = cols, rows
	s.vt.Resize(cols, rows)
	return pty.Setsize(s.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Render returns the current emulator screen as newline-joined text.
// Monochrome for MVP; colors discarded.
func (s *Session) Render() string {
	s.state.Lock()
	defer s.state.Unlock()
	rows, cols := s.state.Size()
	lines := make([]string, 0, rows)
	for y := 0; y < rows; y++ {
		var b strings.Builder
		b.Grow(cols)
		for x := 0; x < cols; x++ {
			ch, _, _ := s.state.Cell(x, y)
			if ch == 0 {
				ch = ' '
			}
			b.WriteRune(ch)
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// Done fires when the child exits.
func (s *Session) Done() <-chan struct{} { return s.done }

// Close kills the child and releases the PTY.
func (s *Session) Close() error {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	err := s.ptmx.Close()
	s.once.Do(func() { close(s.done) })
	return err
}
