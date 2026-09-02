// The transient notice: one line, bottom-right, gone on its own.
//
// Every user-facing one-off message goes through this module and the hook in
// useNotice.tsx. The rules, so the messages do not scatter again:
//
//   1. A notice is an echo of something that already happened, never the only
//      place a fact lives. Anything the user must act on -- a refused drop, a
//      failed validation, a changed object -- is shown where the action is (the
//      dock's caption, the row, the bar) and is not repeated here.
//   2. Two tones and nothing else. `info` confirms; `error` reports a refusal or
//      a failure. The tone decides colour, role and how long it stays.
//   3. Nobody sets notice state directly. `notify(text)` classifies, `dismiss()`
//      clears. The hook owns the timer; components own nothing.
//   4. A new notice replaces the old one. There is no queue and no history: an
//      operation's real feedback is in place, the notice is the afterglow.

export type NoticeTone = "info" | "error";

export type Notice = {
  // Monotonic. The timer is keyed on it, so a repeated identical message still
  // restarts the clock rather than being treated as the same notice.
  id: number;
  text: string;
  tone: NoticeTone;
};

// Classification is by wording rather than by a flag at 39 call sites, which is
// the point: a call site cannot forget to pass the tone, and the vocabulary of
// failure is small and stable in this app. Extend the pattern, not the callers.
const errorPattern = /失败|无法|不能|不接受|未通过|已中止|请先|已停用|没有可加入/;

export function noticeTone(text: string): NoticeTone {
  return errorPattern.test(text) ? "error" : "info";
}

// Long enough to read twice; a failure gets twice that because it is the one
// the user did not expect and may want to quote.
export const noticeDurations: Record<NoticeTone, number> = { info: 4000, error: 8000 };

export function noticeDuration(tone: NoticeTone): number {
  return noticeDurations[tone];
}
