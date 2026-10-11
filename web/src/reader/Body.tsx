import { RiRefreshLine } from "@remixicon/react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { ReaderPage as api, events, type ReaderBody, type Theme } from "@/mygo";
import { makeT } from "@/shared/i18n";
import { forwardShortcuts } from "@/shared/shortcuts";
import { applyTheme, type Theme as PageTheme } from "@/shared/theme";
import { buildSrcdoc } from "./frame";
import { wheelForwarder } from "./wheel";

const ignore = () => {};
const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

/**
 * The reader page: one expanded message's body (role "reader"), embedded in the native reader.
 *
 * Listeners first (readerBody, theme), then load: mygo drops events sent before a listener
 * exists, and only a body newer than the one shown applies (a load resolving after a readerBody is
 * dropped). A theme event wins over the theme of a body that arrives later.
 */
export function ReaderApp() {
  const [body, setBody] = useState<ReaderBody>();
  const [theme, setTheme] = useState<Theme>();
  const [failed, setFailed] = useState<string>();
  const load = useRef<() => void>(ignore);

  useEffect(() => {
    let live = true;
    let version = 0;
    let pushed = false; // a theme event came: newer than any body's theme
    const apply = (b: ReaderBody) => {
      if (!live || b.version <= version) return;
      version = b.version;
      setBody(b);
      setFailed(undefined);
      if (!pushed) setTheme(b.theme);
    };
    const offs = [
      events.readerBody.on(apply),
      events.theme.on((t) => {
        pushed = true;
        setTheme(t);
      }),
    ];
    load.current = () => {
      setFailed(undefined);
      api.load().then(apply, (e) => {
        if (live && !version) setFailed(message(e));
      });
    };
    load.current();
    return () => {
      live = false;
      for (const off of offs) off();
    };
  }, []);

  useEffect(() => forwardShortcuts((dir) => api.navigate(dir)), []);
  useEffect(() => (theme ? applyTheme(theme as PageTheme) : undefined), [theme]);

  if (failed !== undefined)
    return <Failed error={failed} retry={() => load.current()} text={body?.text} />;
  if (!body || !theme) return <main />;
  return (
    <main>
      <Body body={body} theme={theme} />
    </main>
  );
}

function Failed({
  error,
  retry,
  text,
}: {
  error: string;
  retry: () => void;
  text?: Record<string, string>;
}) {
  const t = useMemo(() => makeT(text ?? {}), [text]);
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    const px = Math.ceil(ref.current?.getBoundingClientRect().height ?? 0);
    if (px > 0) api.height(px).catch(ignore);
  }, []);
  return (
    <main ref={ref} className="flex items-center gap-2 p-3 text-sm text-muted-foreground">
      <span>{error || t("error.unknown")}</span>
      <Button variant="ghost" size="icon-sm" aria-label={t("common.retry")} onClick={retry}>
        <RiRefreshLine />
      </Button>
    </main>
  );
}

/**
 * A body in a sandboxed iframe (no scripts; the srcdoc's own CSP first). The page wires it on each
 * load: its height (to Go and to the iframe, so nothing is clipped), wheels and ⌘[ ⌘] to Go, links
 * to OpenLink, and with Remote each http(s) image through Image.
 */
export function Body({ body, theme }: { body: ReaderBody; theme: Theme }) {
  const srcdoc = useMemo(() => buildSrcdoc(body, theme), [body, theme]);
  const frame = useRef<HTMLIFrameElement>(null);
  const remote = body.remote;

  // One forwarder for both windows, so a frame's wheels are summed once.
  const wheel = useMemo(() => wheelForwarder((dy) => void api.wheel(dy).catch(ignore)), []);
  useEffect(() => {
    addEventListener("wheel", wheel, { passive: false });
    return () => removeEventListener("wheel", wheel);
  }, [wheel]);

  const wire = useCallback((f: HTMLIFrameElement) => attach(f, remote, wheel), [remote, wheel]);
  useEffect(() => {
    const f = frame.current;
    if (!f) return;
    let detach = ignore;
    const onLoad = () => {
      detach();
      detach = wire(f);
    };
    f.addEventListener("load", onLoad);
    // Rewired with the document as it is: an unchanged srcdoc doesn't load again (a loading one
    // still fires load, which rewires the new document).
    onLoad();
    return () => {
      f.removeEventListener("load", onLoad);
      detach();
    };
  }, [wire]);

  return (
    <iframe
      ref={frame}
      title="body"
      sandbox="allow-same-origin"
      scrolling="no"
      srcDoc={srcdoc}
      style={{ border: 0, width: "100%", display: "block" }}
    />
  );
}

/** Wires a loaded body document; returns the unwiring. */
function attach(
  frame: HTMLIFrameElement,
  remote: boolean,
  wheel: (e: WheelEvent) => void,
): () => void {
  const doc = frame.contentDocument;
  const win = frame.contentWindow;
  const wrap = doc?.getElementById("godwit-body");
  if (!doc || !win || !wrap) return ignore; // the initial about:blank
  let live = true;
  const offs: (() => void)[] = [];
  const on = <E extends Event>(
    t: EventTarget,
    type: string,
    f: (e: E) => void,
    o?: AddEventListenerOptions,
  ) => {
    t.addEventListener(type, f as EventListener, o);
    offs.push(() => t.removeEventListener(type, f as EventListener, o));
  };

  // Height: the wrapper's own (not scrollHeight, which never shrinks below the viewport), so
  // sizing the iframe to it can't feed back. One call per frame, when it changed.
  let last = -1;
  let queued = false;
  const measure = () => {
    const cs = win.getComputedStyle(doc.body);
    const ws = win.getComputedStyle(wrap);
    const px = Math.ceil(
      wrap.getBoundingClientRect().height +
        [
          cs.marginTop,
          cs.marginBottom,
          cs.paddingTop,
          cs.paddingBottom,
          ws.marginTop,
          ws.marginBottom,
        ]
          .map((v) => Number.parseFloat(v) || 0)
          .reduce((a, b) => a + b, 0),
    );
    if (px === last) return;
    last = px;
    frame.style.height = `${px}px`;
    api.height(px).catch(ignore);
  };
  const schedule = () => {
    if (queued) return;
    queued = true;
    requestAnimationFrame(() => {
      queued = false;
      if (live) measure();
    });
  };
  const ro = new ResizeObserver(schedule);
  ro.observe(wrap);
  offs.push(() => ro.disconnect());
  on(window, "resize", schedule);
  for (const img of doc.images) {
    on(img, "load", schedule);
    on(img, "error", schedule);
  }
  schedule();

  // Links: data-href only (Go's sanitizer); nothing else navigates.
  const linkOf = (t: EventTarget | null) =>
    (t as Element | null)?.closest?.("[data-href]")?.getAttribute("data-href") ?? null;
  for (const el of doc.querySelectorAll("[data-href]")) {
    el.setAttribute("tabindex", "0");
    el.setAttribute("role", "link");
  }
  const open = (href: string) => void api.openLink(href).catch(ignore);
  on<MouseEvent>(doc, "click", (e) => {
    const href = linkOf(e.target);
    if (href !== null) open(href);
    if (href !== null || (e.target as Element | null)?.closest?.("a,area")) e.preventDefault();
  });
  on<KeyboardEvent>(doc, "keydown", (e) => {
    if (e.key !== "Enter" || e.isComposing) return;
    const href = linkOf(e.target);
    if (href === null) return;
    e.preventDefault();
    open(href);
  });

  on(win, "wheel", wheel, { passive: false });
  offs.push(forwardShortcuts((dir) => api.navigate(dir), win));

  // Remote images (allowed): no request leaves (the frame's CSP blocks it anyway); Go fetches
  // each as a data: URL. A failure leaves the alt.
  if (remote) {
    for (const img of [...doc.images]) {
      const src = img.getAttribute("src") ?? "";
      if (!/^https?:/i.test(src)) continue;
      img.setAttribute("src", "");
      api.image(src).then((d) => {
        if (live && d.startsWith("data:")) img.setAttribute("src", d);
      }, ignore);
    }
  }

  return () => {
    live = false;
    for (const off of offs) off();
  };
}
