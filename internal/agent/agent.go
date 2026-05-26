// Package agent abstracts which CLI tool drives an iteration. Saturn
// supports `claude` and `opencode`; per-task choice via task front matter
// `backend:` field, with auto-detect fallback (opencode preferred).
package agent

import (
	"fmt"
	"os/exec"
	"strings"
)

const (
	BackendClaude   = "claude"
	BackendOpencode = "opencode"
)

// ModelInfo describes one model offered by a backend, with the variant
// (effort/reasoning) tiers it supports. Saturn surfaces these in the new-task
// huh form so the user can pick model + tier without memorizing flag names.
//
// Variants intentionally use the *opencode* vocabulary (high/medium/low/
// minimal/max) and are translated at spawn time when the backend is claude
// (which calls the same concept --effort and accepts low/medium/high/xhigh/
// max). "" means "let the backend pick its default", which is what we want
// when the user doesn't override.
type ModelInfo struct {
	// ID is what gets passed to `claude --model` / `opencode --model`.
	// For opencode this is `provider/model`; for claude it's the alias
	// (`opus`, `sonnet`, `haiku`) or full model name.
	ID string
	// Label is what we show in the form. Keeps the dropdown human-readable.
	Label string
	// Variants are the supported reasoning/effort tiers, in display order.
	// Empty slice = backend doesn't expose tiers for this model. The first
	// entry is treated as the default if the user picks the model without
	// picking a variant.
	Variants []string
}

// Catalog returns the hardcoded model list for each backend. Kept inline
// (no config file, no network) per AGENTS.md's stdlib-first preference;
// updates are a one-line PR. Unknown backends return nil.
//
// Sources:
//   - claude: `claude --help` lists `--model` (aliases sonnet/opus/haiku
//     plus full names like claude-sonnet-4-6) and `--effort` taking
//     low|medium|high|xhigh|max.
//   - opencode: `opencode run --help` lists `-m provider/model` and
//     `--variant` taking provider-specific tiers (high|max|minimal|...).
func Catalog(backend string) []ModelInfo {
	switch Resolve(backend) {
	case BackendClaude:
		// Aliases first since they're the most common; full names below for
		// pinning. Variants follow claude's --effort vocabulary directly so
		// translation is a no-op at spawn time.
		return []ModelInfo{
			{ID: "opus", Label: "opus (alias, latest)", Variants: []string{"high", "medium", "low", "xhigh", "max"}},
			{ID: "sonnet", Label: "sonnet (alias, latest)", Variants: []string{"high", "medium", "low", "xhigh", "max"}},
			{ID: "haiku", Label: "haiku (alias, latest)", Variants: []string{"medium", "low"}},
			{ID: "claude-opus-4-7", Label: "claude-opus-4-7", Variants: []string{"high", "medium", "low", "xhigh", "max"}},
			{ID: "claude-sonnet-4-7", Label: "claude-sonnet-4-7", Variants: []string{"high", "medium", "low", "xhigh", "max"}},
		}
	case BackendOpencode:
		// Opencode uses provider/model strings. Pull the common ones; users
		// who want something exotic can still set it via `model:` in front
		// matter — the form just won't autocomplete it.
		return []ModelInfo{
			{ID: "anthropic/claude-opus-4-7", Label: "anthropic · opus-4-7", Variants: []string{"high", "max", "medium", "minimal"}},
			{ID: "anthropic/claude-sonnet-4-7", Label: "anthropic · sonnet-4-7", Variants: []string{"high", "max", "medium", "minimal"}},
			{ID: "openai/gpt-5", Label: "openai · gpt-5", Variants: []string{"high", "medium", "minimal"}},
			{ID: "openai/gpt-5-mini", Label: "openai · gpt-5-mini", Variants: []string{"high", "medium", "minimal"}},
			{ID: "google/gemini-2.5-pro", Label: "google · gemini-2.5-pro", Variants: []string{"high", "medium"}},
		}
	}
	return nil
}

// translateVariant converts saturn's internal variant vocabulary to the
// flag value the active backend expects. Currently the two converge — both
// claude `--effort` and opencode `--variant` accept the literal strings
// high/medium/low/minimal/max — but isolating it here means future divergence
// (e.g. provider-specific aliases) becomes a one-place change.
func translateVariant(backend, variant string) string {
	if variant == "" {
		return ""
	}
	switch Resolve(backend) {
	case BackendClaude:
		// claude's --effort accepts low/medium/high/xhigh/max. Opencode's
		// "minimal" has no direct claude equivalent; map it to "low" so the
		// task still runs (rather than erroring on an unknown effort).
		if variant == "minimal" {
			return "low"
		}
		return variant
	default:
		return variant
	}
}

// Available reports whether the named backend's binary is on PATH.
func Available(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// Resolve returns name if non-empty, otherwise picks the default
// (opencode if available, else claude).
func Resolve(name string) string {
	if name != "" {
		return name
	}
	if Available(BackendOpencode) {
		return BackendOpencode
	}
	return BackendClaude
}

// SpawnOptions are the per-iteration knobs that callers (loop.Drive) pass
// to SpawnCmd. Bundled into a struct so the signature doesn't keep growing
// every time we expose a new backend flag (model, variant today; potentially
// max-budget, agent name, etc. tomorrow).
type SpawnOptions struct {
	Runner  string // custom shell command; receives Prompt on stdin when set
	Backend string // "" = auto
	Prompt  string
	Workdir string
	Model   string // "" = backend default
	Variant string // "" = backend default; saturn vocabulary, translated per-backend
}

// SpawnCmd builds the headless run command for the given backend.
//
// The model and variant args are forwarded to the right backend flag:
//   - claude: --model <id>, --effort <variant>
//   - opencode: --model <id>, --variant <variant>
//
// translateVariant() handles backend-specific vocabulary differences. When
// model or variant is "", the flag is omitted and the backend uses its
// default (matches today's behavior for older tasks without these fields).
func SpawnCmd(opts SpawnOptions) (*exec.Cmd, error) {
	if opts.Runner != "" {
		cmd := exec.Command("sh", "-c", opts.Runner)
		cmd.Dir = opts.Workdir
		cmd.Stdin = strings.NewReader(opts.Prompt)
		return cmd, nil
	}

	switch Resolve(opts.Backend) {
	case BackendOpencode:
		args := []string{"run", opts.Prompt,
			"--format", "json",
			"--dangerously-skip-permissions"}
		if opts.Model != "" {
			args = append(args, "--model", opts.Model)
		}
		if v := translateVariant(opts.Backend, opts.Variant); v != "" {
			args = append(args, "--variant", v)
		}
		cmd := exec.Command("opencode", args...)
		cmd.Dir = opts.Workdir
		return cmd, nil
	case BackendClaude:
		args := []string{"-p", opts.Prompt,
			"--output-format", "stream-json",
			"--verbose",
			"--dangerously-skip-permissions"}
		if opts.Model != "" {
			args = append(args, "--model", opts.Model)
		}
		if v := translateVariant(opts.Backend, opts.Variant); v != "" {
			args = append(args, "--effort", v)
		}
		cmd := exec.Command("claude", args...)
		cmd.Dir = opts.Workdir
		return cmd, nil
	default:
		return nil, fmt.Errorf("unknown backend %q", opts.Backend)
	}
}

// AttachCmd builds the interactive resume command for an existing session.
func AttachCmd(backend, sessionID string) *exec.Cmd {
	switch Resolve(backend) {
	case BackendOpencode:
		return exec.Command("opencode", "run", "-s", sessionID)
	case BackendClaude:
		return exec.Command("claude", "--resume", sessionID)
	}
	return exec.Command("claude", "--resume", sessionID)
}
