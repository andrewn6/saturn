// Package ptty wraps a creack/pty + vt10x emulator pair so callers can drive
// an interactive child process and render its screen inside another TUI
// without fighting the host terminal.
package ptty

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/ActiveState/vt10x"
	"github.com/charmbracelet/lipgloss"
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

// Render returns the current emulator screen with color + cursor overlay as
// a single string containing ANSI SGR escapes. The caller should emit it
// verbatim; Bubble Tea's renderer passes ANSI through unchanged.
func (s *Session) Render() string {
	s.state.Lock()
	defer s.state.Unlock()
	rows, cols := s.state.Size()
	cx, cy := s.state.Cursor()
	showCursor := s.state.CursorVisible()

	var out strings.Builder
	for y := 0; y < rows; y++ {
		out.WriteString(renderRow(s.state, y, cols, cx, cy, showCursor))
		if y < rows-1 {
			out.WriteByte('\n')
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

// renderRow emits one screen row, grouping contiguous cells with identical
// (fg, bg) into a single lipgloss span. Overlays the cursor cell with
// reverse video.
func renderRow(st *vt10x.State, y, cols, cx, cy int, showCursor bool) string {
	var out strings.Builder
	var runBuf strings.Builder
	var runFG, runBG vt10x.Color
	runInit := false

	flush := func() {
		if runBuf.Len() == 0 {
			return
		}
		out.WriteString(styleFor(runFG, runBG, false).Render(runBuf.String()))
		runBuf.Reset()
	}

	for x := 0; x < cols; x++ {
		ch, fg, bg := st.Cell(x, y)
		if ch == 0 {
			ch = ' '
		}
		isCursor := showCursor && x == cx && y == cy
		if isCursor {
			// Flush current run, emit cursor cell by itself.
			flush()
			out.WriteString(styleFor(fg, bg, true).Render(string(ch)))
			runInit = false
			continue
		}
		if !runInit || fg != runFG || bg != runBG {
			flush()
			runFG, runBG, runInit = fg, bg, true
		}
		runBuf.WriteRune(ch)
	}
	flush()
	return strings.TrimRight(out.String(), " ")
}

// styleFor maps a vt10x cell's (fg, bg) to a lipgloss style. If reverse is
// true, swaps fg/bg to emulate a cursor/selected cell.
func styleFor(fg, bg vt10x.Color, reverse bool) lipgloss.Style {
	s := lipgloss.NewStyle()
	fgColor, fgOk := cellColor(fg)
	bgColor, bgOk := cellColor(bg)
	if reverse {
		fgOk, bgOk = bgOk, fgOk
		fgColor, bgColor = bgColor, fgColor
		if !fgOk && !bgOk {
			// No cell colors at all → use terminal reverse.
			return s.Reverse(true)
		}
	}
	if fgOk {
		s = s.Foreground(fgColor)
	}
	if bgOk {
		s = s.Background(bgColor)
	}
	return s
}

// cellColor translates a vt10x.Color to a lipgloss.Color. Returns ok=false
// when the color is the terminal default (0xff80 / 0xff81) so callers can
// skip setting it and inherit the host palette.
func cellColor(c vt10x.Color) (lipgloss.Color, bool) {
	// DefaultFG == 0xff80, DefaultBG == 0xff81 per vt10x constants.
	if uint16(c) >= 0xff80 {
		return "", false
	}
	return lipgloss.Color(strconv.Itoa(int(c))), true
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
