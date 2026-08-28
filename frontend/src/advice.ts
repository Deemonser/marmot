// Which suggestions may be staged without the user having read them.
//
// The panel used to require one click per suggestion. On this machine the rule
// layer finds 58 of them, so acting on the result meant 58 clicks -- and a
// suggestion nobody has the patience to accept is the same as no suggestion.
//
// But "stage everything" is the wrong correction. The whole disruption axis
// exists because identical bytes of build output are free or expensive depending
// on whether the surrounding work is live, and `review` is how that difference
// reaches the user. Auto-staging a `review` item and letting one keypress send it
// to the trash throws away the axis and lands exactly on the failure it was built
// to prevent: an active project losing the cache it is about to need.
//
// So three conditions, each for its own reason:
//
//   source === "rule"  -- deterministic and reproducible. An advisor suggestion
//                         is a claim by a model that got recoverability wrong 5%
//                         of the time (R-063 §3.7); it stays a decision the user
//                         makes with the reasons in front of them, which is also
//                         what ADR-0061 §1 means by advice without authorisation.
//   risk === "safe"    -- `review` means "look at this", and a pre-filled cart is
//                         the opposite of looking.
//   recovery !== irreplaceable
//                      -- belt and braces. A rule should never pair safe with
//                         irreplaceable, and if one ever does, that combination
//                         must not be what silently fills the cart.
export type StageableItem = {
  source: string;
  risk: string;
  recovery: string;
};

export function autoStageable(item: StageableItem): boolean {
  return item.source === "rule" && item.risk === "safe" && item.recovery !== "irreplaceable";
}

// stageSummary phrases what was staged and what deliberately was not, so the
// pre-filled cart is never a surprise the user has to discover in the dock.
export function stageSummary(staged: number, stagedBytes: string, remaining: number): string {
  if (staged === 0 && remaining === 0) return "";
  if (staged === 0) return "这些都需要你确认，没有自动加入的项。";
  const head = "已自动加入 " + staged + " 项可安全清理 · " + stagedBytes;
  return remaining > 0 ? head + "，其余 " + remaining + " 项需要你确认" : head;
}
