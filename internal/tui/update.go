package tui

import (
	"strings"

	"github.com/andrewn6/saturn/internal/selfupdate"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Update flow:
//   1. On TUI init, fire updateCheckCmd in the background. Result returns as
//      updateCheckMsg and lights up the header tag.
//   2. User presses `u` (or picks "Upgrade Saturn" in palette) → openUpdate
//      sets mode=modeUpdate, status=updateStatusReady.
//   3. Inside the modal, user presses enter → updateApplyCmd runs
//      selfupdate.Apply (blocking download). Result comes back as
//      updateApplyMsg.
//   4. After success, we hold the modal open with a "restart saturn" message
//      because the running binary's been replaced — re-execing in-place
//      from inside Bubble Tea is messy and often leaves the terminal
//      half-attached. Quitting and re-launching is the safe path.

type updateStatus int

const (
	updateStatusIdle    updateStatus = iota // no check yet, or check in flight
	updateStatusCurrent                     // checked, on latest
	updateStatusReady                       // newer release available
	updateStatusApplying
	updateStatusDone   // upgrade finished, restart pending
	updateStatusFailed // upgrade hit an error; err holds the message
)

// updateState lives on model. Kept small on purpose; the modal's own UI
// is computed at render time, no widgets to manage.
type updateState struct {
	status        updateStatus
	currentVer    string
	latestVer     string
	err           string
	checkAttempts int // bumps so we don't infinite-loop on transient API failures
}

// Messages produced by the async commands. Kept private — only update.go
// touches them, layout.go just reads m.update.* for the header tag.

type updateCheckMsg struct {
	latest    string
	hasUpdate bool
	err       error
}

type updateApplyMsg struct {
	tag string
	did bool
	err error
}

func updateCheckCmd(currentVer string) tea.Cmd {
	return func() tea.Msg {
		latest, has, err := selfupdate.Check(currentVer)
		return updateCheckMsg{latest: latest, hasUpdate: has, err: err}
	}
}

func updateApplyCmd(currentVer string) tea.Cmd {
	return func() tea.Msg {
		tag, did, err := selfupdate.Apply(currentVer)
		return updateApplyMsg{tag: tag, did: did, err: err}
	}
}

// openUpdate is called from the main key handler (`u`) and the command
// palette ("Upgrade Saturn"). If we have no info yet, kick off a fresh
// check; otherwise jump straight into the modal.
func (m model) openUpdate() (tea.Model, tea.Cmd) {
	m.mode = modeUpdate
	if m.update.status == updateStatusIdle && m.update.latestVer == "" {
		// Bump attempts so the modal can show "checking…" not "idle".
		m.update.checkAttempts++
		return m, updateCheckCmd(m.update.currentVer)
	}
	return m, nil
}

func (m model) updateUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
	case "esc", "q":
		// After a successful apply we leave the modal open until the user
		// quits saturn. esc still exits the modal; they can restart later.
		m.mode = modeList
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		switch m.update.status {
		case updateStatusReady:
			m.update.status = updateStatusApplying
			m.update.err = ""
			return m, updateApplyCmd(m.update.currentVer)
		case updateStatusDone:
			// Quit so the user can re-launch the new binary cleanly.
			return m, tea.Quit
		case updateStatusFailed:
			// Retry.
			m.update.status = updateStatusApplying
			m.update.err = ""
			return m, updateApplyCmd(m.update.currentVer)
		}
	}
	return m, nil
}

// applyUpdateCheck mutates state from a background check result. Called
// from the top-level Update switch in tui.go.
func (m *model) applyUpdateCheck(msg updateCheckMsg) {
	if msg.err != nil {
		// Silent fail — we don't want a network blip to spam the user.
		// The header tag just stays hidden.
		return
	}
	m.update.latestVer = msg.latest
	if msg.hasUpdate {
		m.update.status = updateStatusReady
	} else {
		m.update.status = updateStatusCurrent
	}
}

func (m *model) applyUpdateResult(msg updateApplyMsg) {
	if msg.err != nil {
		m.update.status = updateStatusFailed
		m.update.err = msg.err.Error()
		return
	}
	if !msg.did {
		// Race: by the time we applied, we were already current.
		m.update.status = updateStatusCurrent
		return
	}
	m.update.status = updateStatusDone
	m.update.latestVer = msg.tag
}

// Header tag — used by layout.go's viewListNew. Returns "" when we have
// nothing to surface so the header stays clean.
func (m model) updateHeaderTag() string {
	if m.update.status != updateStatusReady {
		return ""
	}
	tag := m.update.latestVer
	if tag == "" {
		tag = "available"
	}
	return updateHeaderStyle.Render("⇪ update " + tag + " (u)")
}

var (
	updateHeaderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("220")).
				Bold(true)
	updateBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("220")).
			Padding(1, 2)
	updateOK   = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	updateWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true)
	updateErr  = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	updateDim  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)

func (m model) viewUpdate() string {
	w := m.width
	h := m.height
	if w == 0 {
		w, h = 100, 30
	}
	boxW := 64
	if boxW > w-4 {
		boxW = w - 4
	}

	var b strings.Builder
	b.WriteString(updateWarn.Render("▎ Saturn update") + "\n\n")

	cur := m.update.currentVer
	if cur == "" {
		cur = "dev"
	}
	b.WriteString(updateDim.Render("current: ") + cur + "\n")
	if m.update.latestVer != "" {
		b.WriteString(updateDim.Render("latest:  ") + m.update.latestVer + "\n")
	}
	b.WriteString("\n")

	switch m.update.status {
	case updateStatusIdle:
		b.WriteString(updateDim.Render("checking GitHub for releases…"))
	case updateStatusCurrent:
		b.WriteString(updateOK.Render("✓ you're on the latest release") + "\n\n")
		b.WriteString(updateDim.Render("esc to close"))
	case updateStatusReady:
		b.WriteString("a newer release is available.\n\n")
		b.WriteString(updateDim.Render("enter to download and replace this binary · esc to cancel"))
	case updateStatusApplying:
		b.WriteString(spinnerFrame() + " downloading and replacing binary…\n\n")
		b.WriteString(updateDim.Render("(this can take 10-30s)"))
	case updateStatusDone:
		b.WriteString(updateOK.Render("✓ upgraded to "+m.update.latestVer) + "\n\n")
		b.WriteString("the running binary has been replaced, but this saturn\n")
		b.WriteString("process is still the old one. ")
		b.WriteString(updateWarn.Render("enter to quit") + updateDim.Render(" — then re-run saturn watch."))
	case updateStatusFailed:
		b.WriteString(updateErr.Render("✗ upgrade failed") + "\n\n")
		b.WriteString(updateDim.Render(m.update.err) + "\n\n")
		b.WriteString(updateDim.Render("enter to retry · esc to cancel"))
	}

	box := updateBoxStyle.Width(boxW).Render(b.String())
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}
