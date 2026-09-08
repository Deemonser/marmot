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

// analysisAdmits is which findings an analysis puts in the dock at all. Safe
// arrives ticked, review arrives unticked and waits for the person; risky is not
// admitted -- the dock is a list of things about to be deleted, and a risky
// finding is something the catalog is warning about, not offering. Nothing
// irreplaceable is admitted whatever its tier, and nothing root-owned: staging
// either would put a row in the dock that must not, or cannot, be acted on.
export function analysisAdmits(item: StageableItem): boolean {
  if (item.manual || item.recovery === "irreplaceable") return false;
  return item.risk === "safe" || item.risk === "review";
}

// stageSummary phrases what was staged and what deliberately was not, so the
// pre-filled cart is never a surprise the user has to discover in the dock.
// One short sentence about what the app did on the user's behalf. It does not
// repeat what is already on screen: the staged count is the 已收集 header, the
// bytes are the badge, the remaining count is the 全部加入 button.
// riskColor is the colour a row from analysis arrives with -- the risk tier the
// old 待确认 list painted on its dot (green / amber / red), now on the checkbox.
// A dragged row keeps its slice colour instead: it says where the thing is, not
// how safe it is, and the two must not be confused. Unknown tiers fall through
// to undefined so the checkbox shows its default and nothing is claimed.
// draggedColor is the one colour every dragged row arrives with. The slice
// colour was tried and dropped: beside the risk tiers it reads as a fourth
// tier that means nothing, and two greens in one list would be a lie. Blue is
// the dock's own colour (the badge ring, the filled target), so it says
// "you put this here" and nothing about how safe it is.
export const draggedColor = "#6b8fc3";

const riskColors: Record<string, string> = { safe: "#7fb96a", review: "#d3a44b", risky: "#d0453a" };
export function riskColor(risk: string): string | undefined {
  return riskColors[risk];
}

export function stageSummary(staged: number, remaining: number): string {
  if (staged === 0 && remaining === 0) return "";
  if (staged === 0) return "没有可自动加入的项，都需要你确认。";
  return "已自动加入 " + staged + " 项安全项。";
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

// Why a suggestion sits at its tier. Risk is derived from facts on the Go side
// (ADR-0067) and each conclusion carries the codes that produced it, so
// "需确认" can say which of several very different facts it stands for: a
// project in use, a generic rule that cannot name the object, login state, a
// model that was not sure. Unknown codes are shown as themselves rather than
// dropped -- a reason the UI cannot translate is still a reason.
const riskReasonLabels: Record<string, string> = {
  irreplaceable: "删除后无法恢复",
  login_state: "包含登录状态",
  partial_install: "工具链内部目录",
  project_active: "项目正在使用",
  project_dormant: "项目已停摆",
  cache_cold: "缓存长期未使用",
  generation_superseded: "已有更新版本",
  redownload_cost: "需要重新下载",
  advisor_uncertain: "AI 把握不足",
  generic_rule: "泛规则匹配",
  catalog: "规则标注需确认",
};

export function riskReasonLabel(code: string): string {
  return riskReasonLabels[code] ?? code;
}

// The tags shown inline are the ones that carry a decision. A reason that only
// restates a tag already on the row is not one of them -- a row wearing
// 不可恢复, 高风险 and 删除后无法恢复 says one thing three times, and the reader
// stops reading tags. Each of these has exactly one other tag that already
// carries it:
//   irreplaceable    -> the recovery tag is 不可恢复
//   redownload_cost  -> the recovery tag is 可重新下载 (Assess only adds this
//                       reason when recovery is redownloadable)
//   advisor_uncertain-> the AI tag shows the confidence, and goes grey below
//                       the threshold that produced this code
//   catalog, generic_rule -> only say the rule was cautious, which the tier says
// All of them stay in the detail view, which is the place that lists every
// reason rather than the ones that change a decision.
const detailOnlyReasons = new Set([
  "catalog",
  "generic_rule",
  "irreplaceable",
  "redownload_cost",
  "advisor_uncertain",
]);

export function inlineRiskReasons(reasons: string[] | null | undefined): string[] {
  return (reasons ?? []).filter((code) => !detailOnlyReasons.has(code));
}
