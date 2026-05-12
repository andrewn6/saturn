// Package slug produces deterministic, filesystem-and-git-branch-safe
// identifiers from human strings.
//
// The id matters because saturn uses it three different ways:
//
//   - As a directory name under .saturn/wt/<id>/ and .saturn/runs/<id>/.
//   - As a git branch suffix on saturn/<id>.
//   - As the front-matter `id:` key, which the TUI re-derives from the title
//     when writing new task files.
//
// Before this package existed there were two independent slugifiers
// (internal/task and internal/tui) with a comment in the TUI explicitly
// noting "keep the two implementations identical or things will diverge."
// One source now; callers from anywhere can import it.
package slug

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Make returns the slug for s: lowercased, with runs of non-[a-z0-9]
// collapsed to a single '-' and leading/trailing '-' trimmed. If the input
// produces an empty slug (all-punctuation, whitespace-only, etc) Make
// returns "" — the caller decides whether to fall back to a default.
//
// No length cap, no uniqueness guarantee. Callers that use the slug as a
// branch or directory name must handle collisions themselves; saturn so
// far relies on the assumption that humans pick distinct task titles.
func Make(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonAlnum.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// MakeOrFallback is the variant most callers actually want: empty input
// (or input that slugifies to "") yields a time-suffixed default so the
// caller is guaranteed a non-empty, mostly-unique id.
func MakeOrFallback(s, fallbackPrefix string) string {
	if out := Make(s); out != "" {
		return out
	}
	return fmt.Sprintf("%s-%d", fallbackPrefix, time.Now().Unix())
}
