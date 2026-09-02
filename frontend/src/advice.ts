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
  // manual findings are root-owned paths this app cannot delete. Staging one
  // would put an item in the dock that is guaranteed to fail, which is worse than
  // not offering it: the user acts, waits, and gets a permission error.
  manual?: boolean;
};

export function autoStageable(item: StageableItem): boolean {
  if (item.manual) return false;
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

// The dock has two sections and an object is in exactly one of them (ADR-0066
// §2): a suggestion that has been collected leaves "待确认" for "已收集", and one
// taken back out of the dock returns with a mark. The mark matters to the bulk
// button: "全部加入" acting on a list the user can see is what separates it from
// pre-filling the cart, and a list that quietly re-included what the user had
// just removed would break that promise.
export type PendingItem = StageableItem & { path: string };

// bulkCandidates is what "全部加入 N 项" would add: not manual (it would fail),
// not already collected (nothing to do), not dismissed (the user said no).
export function bulkCandidates<T extends PendingItem>(
  items: T[],
  collected: (item: T) => boolean,
  dismissed: Set<string>,
): T[] {
  return items.filter((item) => !item.manual && !collected(item) && !dismissed.has(item.path));
}

// sourceLabel names where a suggestion came from, in the words the row shows.
// A rule is named by its rule; a model claim carries its confidence, because
// that number is the only thing separating it from a rule in the user's eyes.
export function sourceLabel(item: { source: string; ruleName: string; category: string; confidence: number }): string {
  if (item.source === "advisor") return "AI · " + Math.round(item.confidence * 100) + "%";
  return item.ruleName || item.category;
}
