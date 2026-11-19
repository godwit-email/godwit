import {
  RiCheckboxCircleLine,
  RiCloseCircleLine,
  RiCloseLine,
  RiErrorWarningLine,
} from "@remixicon/react";
import {
  type CSSProperties,
  type ReactNode,
  type RefObject,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { type ExternalToast, Toaster as Sonner, toast as sonner } from "sonner";

// Godwit's toast, as the native kit's (internal/kit/toast.go) over sonner: the popover material and
// its hairline, 360 wide in the panel's bottom right corner, at most three stacked, the older ones
// narrower behind the newest until the pointer or the focus spreads them. Only the leading icon
// tells a success, warning or error. The look is in styles.css (.godwit-toaster). Anything that
// calls sonner's own toast (the Wit panel) shows here too.

/** How long a toast stays, in ms; one with an action stays longer, to be reached. */
const PLAIN_MS = 4000;
const ACTION_MS = 8000;
/** The room from the panel's corner, and from a bar's top: theme.Space(4) and Space(2). */
const CORNER = 16;
const ABOVE_BAR = 8;
/** The gap between spread toasts, which is also how much of each older one peeks behind the newest. */
const GAP = 8;
const SHOWN = 3;

function decorate(o?: ExternalToast): ExternalToast | undefined {
  if (!o) return o;
  const out = { ...o };
  if (out.duration === undefined && o.action) out.duration = ACTION_MS;
  if (out.duration === Infinity && out.closeButton === undefined) out.closeButton = true;
  return out;
}

type Message = ReactNode | (() => ReactNode);

/**
 * Shows a toast. A message is required; `description` adds a muted second line, `action` one
 * button (kept 8 s instead of 4), and `duration: Infinity` keeps it until closed, with a close
 * button.
 */
export const toast = Object.assign(
  (message: Message, o?: ExternalToast) => sonner(message, decorate(o)),
  {
    success: (message: Message, o?: ExternalToast) => sonner.success(message, decorate(o)),
    warning: (message: Message, o?: ExternalToast) => sonner.warning(message, decorate(o)),
    error: (message: Message, o?: ExternalToast) => sonner.error(message, decorate(o)),
    dismiss: (id?: number | string) => sonner.dismiss(id),
  },
);

/** Where the stack sits: its distance from the viewport's right and bottom. */
type Inset = { right: number; bottom: number };

/**
 * The stack's inset: the corner's, or, over a bar, 8 above its top and level with its input's
 * right edge, measured as the layout changes (a window resize, a bar that wraps).
 */
function useInset(
  bar?: RefObject<HTMLElement | null>,
  input?: RefObject<HTMLElement | null>,
): Inset {
  const [inset, setInset] = useState<Inset>({ right: CORNER, bottom: CORNER });
  useLayoutEffect(() => {
    if (!bar) return;
    const measure = () => {
      const b = bar.current?.getBoundingClientRect();
      const i = (input ?? bar).current?.getBoundingClientRect();
      const root = document.documentElement;
      const next =
        b && i && b.width > 0 && i.width > 0
          ? { right: root.clientWidth - i.right, bottom: root.clientHeight - b.top + ABOVE_BAR }
          : { right: CORNER, bottom: CORNER };
      setInset((cur) => (cur.right === next.right && cur.bottom === next.bottom ? cur : next));
    };
    measure();
    const ro = new ResizeObserver(measure);
    for (const el of [bar.current, input?.current, document.documentElement]) {
      if (el) ro.observe(el);
    }
    window.addEventListener("resize", measure);
    return () => {
      ro.disconnect();
      window.removeEventListener("resize", measure);
    };
  }, [bar, input]);
  return inset;
}

/**
 * The toast host: once per page. `bar` is a bar along the panel's foot (the compose window's send
 * bar) and `input` the field whose right edge the toasts line up with (the bar if absent): the
 * stack rises 8 above the bar. Without a bar it sits 16 in from the window's corner. `closeLabel`
 * is the close button's label, from the page's own `t`: a second locale subscription here would
 * split the page's.
 */
export function Toaster({
  bar,
  input,
  closeLabel,
}: {
  bar?: RefObject<HTMLElement | null>;
  input?: RefObject<HTMLElement | null>;
  closeLabel: string;
}) {
  const { right, bottom } = useInset(bar, input);
  const host = useRef<HTMLDivElement>(null);

  // Sonner spreads the stack for the pointer alone; the keyboard focus and Enter say the same to it.
  // shortcut: a synthetic pointer event stands in for sonner's internal state, which no prop reaches; drop it when sonner exposes one.
  useEffect(() => {
    const el = host.current;
    if (!el) return;
    const list = () => el.querySelector("[data-sonner-toaster]");
    const inList = (n: EventTarget | null) => n instanceof Node && !!list()?.contains(n);
    const spread = (on: boolean) =>
      list()?.dispatchEvent(new MouseEvent(on ? "mouseover" : "mouseout", { bubbles: true }));
    const focusin = (e: FocusEvent) => inList(e.target) && !inList(e.relatedTarget) && spread(true);
    const focusout = (e: FocusEvent) =>
      inList(e.target) && !inList(e.relatedTarget) && spread(false);
    const enter = (e: KeyboardEvent) =>
      e.key === "Enter" && e.target instanceof HTMLLIElement && spread(true);
    el.addEventListener("focusin", focusin);
    el.addEventListener("focusout", focusout);
    el.addEventListener("keydown", enter);
    return () => {
      el.removeEventListener("focusin", focusin);
      el.removeEventListener("focusout", focusout);
      el.removeEventListener("keydown", enter);
    };
  }, []);

  return (
    <div ref={host} className="contents">
      <Sonner
        className="godwit-toaster"
        position="bottom-right"
        visibleToasts={SHOWN}
        gap={GAP}
        duration={PLAIN_MS}
        offset={{ right, bottom }}
        style={{ "--width": "var(--toast-width)" } as CSSProperties}
        toastOptions={{ closeButtonAriaLabel: closeLabel }}
        icons={{
          success: <RiCheckboxCircleLine data-tone="success" />,
          warning: <RiErrorWarningLine data-tone="warning" />,
          error: <RiCloseCircleLine data-tone="error" />,
          close: <RiCloseLine />,
        }}
      />
    </div>
  );
}
