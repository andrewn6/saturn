package assets

import _ "embed"

//go:embed standing.md
var StandingPrompt string

//go:embed single.md
var SinglePrompt string

//go:embed plan.md
var PlanPrompt string

//go:embed architect.md
var ArchitectPrompt string

//go:embed planner.md
var PlannerPrompt string
