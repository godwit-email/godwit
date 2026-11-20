import { type RefObject, useEffect, useRef } from "react";
import type { Rect } from "@/mygo";

// Toasts are native's (internal/kit/toast.go draws them over the page, as the main window's): a page
// only says what to show and where its bar is. Its NativeToasts says which bridge to send them by.

/** A page's bridge to native's toasts: its ComposePage or WitPage. */
export interface ToastBridge {
  toast(kind: string, text: string): Promise<void>;
  toastBar(bar: Rect, field: Rect): Promise<void>;
}

let bridge: ToastBridge | undefined;

const send = (kind: string) => (text: string) => {
  void bridge?.toast(kind, text).catch(() => {}); // a toast lost is no reason to fail what failed
};

/** Shows text as a toast, drawn by native; its kind's icon tells a success, warning or error. */
export const toast = Object.assign(send(""), {
  success: send("success"),
  warning: send("warning"),
  error: send("error"),
});

const none: Rect = { X: 0, Y: 0, W: 0, H: 0 };

function rect(el: Element | null | undefined): Rect {
  if (!el) return none;
  const r = el.getBoundingClientRect();
  return { X: r.left, Y: r.top, W: r.width, H: r.height };
}

/**
 * Sends the page's toasts by api, and tells native where `bar` (a bar along the page's foot) and
 * `input` (the field its toasts line up with; the bar if absent) are as the layout changes, so the
 * toasts rise 8 above the bar. Once per page.
 */
export function NativeToasts({
  api,
  bar,
  input,
}: {
  api: ToastBridge;
  bar?: RefObject<HTMLElement | null>;
  input?: RefObject<HTMLElement | null>;
}) {
  useEffect(() => {
    bridge = api;
    return () => {
      if (bridge === api) bridge = undefined;
    };
  }, [api]);
  const seen = useRef<{
    api?: ToastBridge;
    els: (Element | null)[];
    ro?: ResizeObserver;
    last: string;
  }>({ els: [], last: "" });
  const report = () => {
    const s = seen.current;
    const b = rect(bar?.current);
    const f = rect(input?.current ?? bar?.current);
    const key = JSON.stringify([b, f]);
    if (key === s.last && api === s.api) return;
    s.last = key;
    s.api = api;
    void api.toastBar(b, f).catch(() => {});
  };
  const live = useRef(report);
  live.current = report;
  // After every commit, every ref attached (an ancestor's too): the bar may have mounted since (the
  // Wit panel's, once its conversation loads).
  useEffect(() => {
    const s = seen.current;
    const els = [bar?.current ?? null, input?.current ?? null];
    if (!s.ro || els[0] !== s.els[0] || els[1] !== s.els[1]) {
      s.ro?.disconnect();
      s.ro = new ResizeObserver(() => live.current());
      for (const el of [...els, document.documentElement]) if (el) s.ro.observe(el);
      s.els = els;
    }
    live.current();
  });
  useEffect(() => {
    const onResize = () => live.current();
    window.addEventListener("resize", onResize);
    return () => {
      window.removeEventListener("resize", onResize);
      seen.current.ro?.disconnect();
    };
  }, []);
  return null;
}
