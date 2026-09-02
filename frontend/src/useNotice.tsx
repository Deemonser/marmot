import { useCallback, useEffect, useRef, useState } from "react";
import { noticeDuration, noticeTone } from "./notice";
import type { Notice } from "./notice";

// useNotice owns the one transient notice and its clock. See notice.ts for the
// rules; this file is the only place that holds notice state or a notice timer.
export function useNotice() {
  const [notice, setNotice] = useState<Notice | null>(null);
  const timer = useRef<number | null>(null);
  const held = useRef(false);
  const serial = useRef(0);

  const clearTimer = () => {
    if (timer.current !== null) window.clearTimeout(timer.current);
    timer.current = null;
  };

  const dismiss = useCallback(() => {
    clearTimer();
    setNotice(null);
  }, []);

  // The clock starts when a notice lands and restarts whenever the pointer
  // leaves it. Holding (pointer over the notice) stops the clock outright: a
  // message being read must not vanish under the reader.
  const arm = useCallback((current: Notice) => {
    clearTimer();
    timer.current = window.setTimeout(() => {
      timer.current = null;
      setNotice((shown) => (shown && shown.id === current.id ? null : shown));
    }, noticeDuration(current.tone));
  }, []);

  useEffect(() => {
    if (!notice || held.current) return;
    arm(notice);
    return clearTimer;
  }, [notice, arm]);

  const notify = useCallback((text: string) => {
    if (!text) {
      dismiss();
      return;
    }
    held.current = false;
    serial.current += 1;
    setNotice({ id: serial.current, text, tone: noticeTone(text) });
  }, [dismiss]);

  const hold = useCallback(() => {
    held.current = true;
    clearTimer();
  }, []);

  const release = useCallback(() => {
    held.current = false;
    setNotice((current) => {
      if (current) arm(current);
      return current;
    });
  }, [arm]);

  return { notice, notify, dismiss, hold, release };
}

// The one rendering of a notice. Click dismisses; hovering holds it.
export function NoticeToast({ notice, onDismiss, onHold, onRelease }: {
  notice: Notice | null;
  onDismiss: () => void;
  onHold: () => void;
  onRelease: () => void;
}) {
  if (!notice) return null;
  return (
    <div
      key={notice.id}
      className={"notice is-" + notice.tone}
      role={notice.tone === "error" ? "alert" : "status"}
      onClick={onDismiss}
      onMouseEnter={onHold}
      onMouseLeave={onRelease}
      title="点击关闭"
    >
      {notice.text}
    </div>
  );
}
