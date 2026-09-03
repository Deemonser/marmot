package recommendation

import (
	"fmt"
	"slices"
)

// Risk used to be written in three places -- the rule catalog, the project
// activity adjustment, and the advisor's own claim corrected by the guards --
// and no one of them could see all the facts. A user shown `review` could not
// tell whether it meant "the rule author was unsure about this generic path",
// "the project is in use", "this looks like login state" or "the model was not
// confident". And the model was still the one producing the label, which
// R-063 §3.5 measured as its least stable output (8/20 drift, against 1/20 on
// recoverability).
//
// So risk is now a conclusion, not an input. Facts are gathered from whoever
// knows them -- the catalog, the guards, the activity signals, the model -- and
// Assess is the one function that turns them into a tier. Rule and advisor
// suggestions go through the same function, which is what makes the advisor's
// risk drift disappear structurally rather than by prompt wording (ADR-0067).

// ActivityKind names the signal behind IdleDays, because "180 days" means
// something different for a project's source, a cache's own writes and a
// superseded generation.
type ActivityKind string

const (
	// No signal. Nothing about the tier is adjusted.
	ActivityNone ActivityKind = ""
	// How long the surrounding project's source has gone untouched (SDD §14.5a).
	ActivityProject ActivityKind = "project_source"
	// How long the object itself has gone unwritten. Meaningful for an app cache,
	// which the app writes whenever it runs; meaningless for a build artifact,
	// whose age says nothing about whether it is about to be rebuilt (R-063 §4b.1).
	ActivityArtifact ActivityKind = "artifact_age"
	// A version-numbered directory a newer sibling has taken over from (§14.5c).
	ActivityGeneration ActivityKind = "generation"
)

// Reason codes. Machine-readable so the UI can translate them and a probe can
// count them. The sentences live in one place only, frontend/src/advice.ts,
// next to the recovery and risk labels: a second table here would drift from
// it with nothing to notice.
const (
	ReasonIrreplaceable        = "irreplaceable"
	ReasonLoginState           = "login_state"
	ReasonPartialInstall       = "partial_install"
	ReasonProjectActive        = "project_active"
	ReasonProjectDormant       = "project_dormant"
	ReasonCacheCold            = "cache_cold"
	ReasonGenerationSuperseded = "generation_superseded"
	ReasonRedownloadCost       = "redownload_cost"
	ReasonAdvisorUncertain     = "advisor_uncertain"
	ReasonGenericRule          = "generic_rule"
	ReasonCatalog              = "catalog"
)

// Facts is everything Assess is allowed to look at. A struct rather than a
// parameter list so that adding a signal is adding a field, and so a test can
// state exactly which facts produced a tier.
type Facts struct {
	Source   Source
	Recovery Recovery
	// Declared is the rule author's baseline. Empty for the advisor, whose
	// baseline is derived from recovery and confidence instead: the model is
	// stable about the first and the second is its own admission.
	Declared   Risk
	Confidence float64
	Activity   ActivityKind
	// IdleDays goes with Activity. Ignored when Activity is none.
	IdleDays int64
	// Guards are the codes returned by IrreplaceableReason, LoginStateReason and
	// PartialInstallReason. Any Irreplaceable* code is terminal.
	Guards []string
	// Generic is the catalog author's declaration that the rule knows the
	// container and not the object (`Library/Caches/*`), see Rule.Generic. Not
	// derived from the pattern: segment counting gets it backwards. The tier is
	// unchanged; the reason is what tells the user why the wording is vague.
	Generic bool
}

// Assessment is what Assess concludes.
type Assessment struct {
	Risk Risk
	// Reasons explain the tier. Never empty for review or risky.
	Reasons []string
	// Note is one sentence about the activity signal, to go in front of
	// what_breaks. Empty when the signal said nothing worth saying.
	Note string
}

// AdvisorConfidentThreshold is the confidence at which a regenerable advisor
// claim is shown as safe rather than review. Advisor items never stage
// themselves whatever the tier (SDD §14.5d), so this decides a colour, not an
// action.
const AdvisorConfidentThreshold = 0.8

// ArtifactColdDays is how long an app cache must go unwritten before it is
// treated as belonging to an app nobody runs. The same round number as
// ProjectDormantDays, and equally unvalidated (R-063 §6).
const ArtifactColdDays = ProjectDormantDays

// Assess derives the tier. The order is fixed and the permanent-loss guards
// come first: a permanent loss is not something an activity signal gets to
// soften.
func Assess(facts Facts) Assessment {
	out := Assessment{}
	if facts.Recovery == RecoveryIrreplaceable || hasIrreplaceableGuard(facts.Guards) {
		out.Reasons = append(out.Reasons, ReasonIrreplaceable)
		switch {
		case facts.Source == SourceAdvisor, facts.Declared == "", hasIrreplaceableGuard(facts.Guards):
			// A model's claim on something permanent, or a location guard firing:
			// the strongest warning there is.
			out.Risk = RiskRisky
		case facts.Declared == RiskSafe:
			// The catalog must never pair these; if it does, the pairing is not
			// what reaches the user.
			out.Risk = RiskReview
		default:
			// The catalog author knew it was permanent and chose the tier with
			// that in mind: emptying the trash is `review`, deleting an iOS
			// backup is `risky`. Their judgement stands.
			out.Risk = facts.Declared
		}
		return withGeneric(out, facts)
	}
	if hasGuard(facts.Guards, LoginState) {
		out.Risk = RiskRisky
		out.Reasons = append(out.Reasons, ReasonLoginState)
		return withGeneric(out, facts)
	}

	out.Risk = baseline(facts)
	// An advisor that was not sure what the object is does not get to be
	// relaxed by a signal about the project around it: dormancy says the bytes
	// will not be missed, not what the bytes are.
	uncertain := facts.Source == SourceAdvisor && facts.Confidence < AdvisorConfidentThreshold

	switch facts.Activity {
	case ActivityProject:
		switch {
		case facts.IdleDays < 0:
			// Outside any recognised project there is no signal, and guessing is
			// how an active project's cache gets called cold.
		case facts.IdleDays <= ProjectActiveDays:
			if out.Risk == RiskSafe {
				out.Risk = RiskReview
			}
			out.Reasons = append(out.Reasons, ReasonProjectActive)
			out.Note = fmt.Sprintf("这个项目 %d 天前还改过源码，正在使用中：删掉之后下一次构建要重新下载或重新编译。", facts.IdleDays)
		case facts.IdleDays >= ProjectDormantDays:
			if out.Risk == RiskReview && !uncertain {
				out.Risk = RiskSafe
			}
			out.Reasons = append(out.Reasons, ReasonProjectDormant)
			out.Note = fmt.Sprintf("这个项目的源码已经 %d 天没有改动。", facts.IdleDays)
		}
	case ActivityArtifact:
		// Relax only. A cache being written this week is a cache in use, and
		// deleting a cache in use costs exactly a rebuild -- which is what `safe`
		// means. Raising it would make every cache on the machine review.
		if facts.IdleDays >= ArtifactColdDays && out.Risk == RiskReview {
			out.Risk = RiskSafe
			out.Reasons = append(out.Reasons, ReasonCacheCold)
			out.Note = fmt.Sprintf("这个缓存已经 %d 天没有被写过，对应的应用应该很久没有运行了。", facts.IdleDays)
		}
	case ActivityGeneration:
		out.Reasons = append(out.Reasons, ReasonGenerationSuperseded)
	}

	// A partial toolchain deletion is a floor, applied after the activity
	// signal so that a dormant project cannot relax it back to safe: the
	// toolchain stays broken however long the project has been idle. Its
	// message already says what happens, and an activity note beside it would
	// say the opposite ("下一次构建要重新下载"), so the note is dropped.
	if hasGuard(facts.Guards, PartialInstall) {
		if out.Risk == RiskSafe {
			out.Risk = RiskReview
		}
		out.Reasons = append(out.Reasons, ReasonPartialInstall)
		out.Note = ""
	}

	if facts.Source == SourceAdvisor && out.Risk == RiskReview {
		if facts.Recovery == RecoveryRedownloadable {
			out.Reasons = append(out.Reasons, ReasonRedownloadCost)
		}
		if uncertain {
			out.Reasons = append(out.Reasons, ReasonAdvisorUncertain)
		}
	}
	out = withGeneric(out, facts)
	if out.Risk != RiskSafe && len(out.Reasons) == 0 {
		out.Reasons = append(out.Reasons, ReasonCatalog)
	}
	return out
}

// baseline is where the tier starts before guards and activity have their say.
func baseline(facts Facts) Risk {
	if facts.Source != SourceAdvisor && facts.Declared != "" {
		return facts.Declared
	}
	switch facts.Recovery {
	case RecoveryRegenerable:
		if facts.Confidence >= AdvisorConfidentThreshold {
			return RiskSafe
		}
		return RiskReview
	case RecoveryRedownloadable:
		return RiskReview
	default:
		return RiskRisky
	}
}

func withGeneric(out Assessment, facts Facts) Assessment {
	if facts.Generic {
		out.Reasons = append(out.Reasons, ReasonGenericRule)
	}
	return out
}

func hasGuard(guards []string, code string) bool {
	return slices.Contains(guards, code)
}

// irreplaceableGuard returns the first permanent-loss code among the guards,
// or "". The set of such codes is whatever IrreplaceableMessage can explain.
func irreplaceableGuard(guards []string) string {
	for _, guard := range guards {
		if IrreplaceableMessage(guard) != "" {
			return guard
		}
	}
	return ""
}

func hasIrreplaceableGuard(guards []string) bool {
	return irreplaceableGuard(guards) != ""
}

// GuardsFor collects every guard code that applies to a path. It is the one
// place the three guard functions are consulted together, so a caller cannot
// remember two of them and forget the third.
func GuardsFor(absolutePath string) []string {
	guards := make([]string, 0, 3)
	if reason := IrreplaceableReason(absolutePath); reason != "" {
		guards = append(guards, reason)
	}
	if reason := LoginStateReason(absolutePath); reason != "" {
		guards = append(guards, reason)
	}
	if reason := PartialInstallReason(absolutePath); reason != "" {
		guards = append(guards, reason)
	}
	return guards
}

// Facts builds the fact sheet a Recommendation carries, so a caller that learns
// one more fact -- the application layer adding an activity signal to an advisor
// item -- can reassess without re-deriving the rest.
func (r Recommendation) Facts() Facts {
	return Facts{
		Source:     r.Source,
		Recovery:   r.Recovery,
		Declared:   r.DeclaredRisk,
		Confidence: r.Confidence,
		Activity:   r.Activity,
		IdleDays:   r.IdleDays,
		Guards:     r.Guards,
		Generic:    r.Generic,
	}
}

// Reassess recomputes Risk and RiskReasons from the facts on the
// recommendation and returns the activity note. It does not touch WhatBreaks:
// the caller decides whether the note has already been put there, which keeps
// the operation idempotent.
func (r *Recommendation) Reassess() string {
	assessment := Assess(r.Facts())
	r.Risk = assessment.Risk
	r.RiskReasons = assessment.Reasons
	return assessment.Note
}
