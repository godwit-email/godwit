import { currentWindow } from "mygo-runtime";
import { useEffect, useRef, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { ComposePage as api, type ComposeState, events } from "@/mygo";
import { applyTheme, type Theme } from "@/shared/theme";
import { createSaver, type Saver } from "./saver";

const list = (s: string) =>
  s
    .split(/[,，;；]/)
    .map((x) => x.trim())
    .filter(Boolean);

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** What a closing needs of the editor: whether input is frozen, the IME composition in progress, and where the settling's blur took focus from. */
interface CloseGate {
  frozen: boolean;
  closings: number;
  ime?: { ended: Promise<void>; end: () => void };
  blurred?: HTMLElement | null;
}

/** The settle hook: a composition in progress commits (blur, up to 1 s); false if it did not. */
async function settle(g: CloseGate): Promise<boolean> {
  const ime = g.ime; // blur may end the composition, clearing g.ime, synchronously
  g.blurred = null;
  if (!ime) return true;
  g.blurred = document.activeElement as HTMLElement | null;
  g.blurred?.blur(); // commits the composition
  const ok = await Promise.race([ime.ended.then(() => true), wait(1000).then(() => false)]);
  await wait(0); // WebKit sends the committed text's input after compositionend
  return ok;
}

/**
 * The compose page: loads the window's draft, autosaves it, flushes when the window closes.
 *
 * A closing freezes input until its answer, so nothing typed after the flush is lost to the
 * window's Destroy. A composition in progress commits first (blur, up to 1 s) so its text is
 * flushed; one still open after that is not saved and is answered saved=false. The page cannot see whether the user kept the window, so: saved=false (the user is
 * asked and may keep it) thaws at once; saved=true thaws only if the window is still alive 2 s
 * later (the answer came too late to count). A newer closing keeps an older one from thawing.
 */
export function ComposePage() {
  const [state, setState] = useState<ComposeState>();
  const [frozen, setFrozen] = useState(false);
  const saver = useRef<Saver>(undefined);
  const gate = useRef<CloseGate>({ frozen: false, closings: 0 });

  useEffect(() => {
    const g = gate.current;
    const freeze = (on: boolean) => {
      g.frozen = on;
      setFrozen(on);
    };
    return events.closing.on(async (gen) => {
      const n = ++g.closings;
      const sv = saver.current; // cleanup may clear the ref while the flush runs
      let focused: HTMLElement | null | undefined;
      // The answer runs in the saver's queue, after the flush: no operation overlaps it.
      const answer = async (saved: boolean) => {
        focused = g.blurred;
        freeze(true);
        await api.closed(gen, saved);
      };
      // An uncommitted composition is not saved and is answered unsaved, so the user is asked.
      let saved = false;
      try {
        if (sv) saved = await sv.flush(answer);
        else {
          await answer(true); // closing before the draft loaded has nothing to flush
          saved = true;
        }
      } catch {
        // the answer did not go out: the window stays, so the page must not stay frozen
      }
      const thaw = () => {
        if (n !== g.closings) return;
        freeze(false);
        focused?.focus(); // back where the blur took it
      };
      if (saved) setTimeout(thaw, 2000);
      else thaw();
    });
  }, []);

  useEffect(() => {
    let current = true; // only the latest load applies (StrictMode loads twice)
    api.load().then((st) => current && setState(st));
    return () => {
      current = false;
    };
  }, []);

  useEffect(() => {
    if (!state) return;
    let stop = applyTheme(state.theme as Theme);
    const off = events.theme.on((t) => {
      stop();
      stop = applyTheme(t as Theme);
    });
    return () => {
      off();
      stop();
    };
  }, [state]);

  if (!state) return <main aria-label="撰写" />;
  return <Editor state={state} saver={saver} gate={gate.current} frozen={frozen} />;
}

function Editor({
  state,
  saver,
  gate,
  frozen,
}: {
  state: ComposeState;
  saver: { current: Saver | undefined };
  gate: CloseGate;
  frozen: boolean;
}) {
  const [f, setF] = useState({
    to: state.to.join(", "),
    cc: state.cc.join(", "),
    bcc: state.bcc.join(", "),
    subject: state.subject,
    body: state.body,
  });
  const [showQuote, setShowQuote] = useState(false);
  const [, rerender] = useState(0);
  const [savedAt, setSavedAt] = useState<Date>();

  // Created in an effect so StrictMode's discarded mount is disposed, not left with a live timer.
  useEffect(() => {
    const n = createSaver(api.save, {
      revision: state.revision,
      snapshot: {
        to: state.to,
        cc: state.cc,
        bcc: state.bcc,
        replyTo: state.replyTo,
        subject: state.subject,
        body: state.body,
        revision: state.revision,
      },
    });
    n.setSettle(() => settle(gate));
    const off = n.onChange(() => {
      if (n.status === "saved") setSavedAt(new Date());
      rerender((i) => i + 1);
    });
    saver.current = n;
    return () => {
      off();
      n.dispose();
      if (saver.current === n) saver.current = undefined;
    };
  }, [state, saver, gate]);
  useEffect(() => {
    currentWindow.setTitle(f.subject.trim() || "新邮件");
  }, [f.subject]);

  const set = (k: keyof typeof f) => (ev: { target: { value: string } }) => {
    if (gate.frozen) return;
    const n = { ...f, [k]: ev.target.value };
    const taken = saver.current?.edit({
      to: list(n.to),
      cc: list(n.cc),
      bcc: list(n.bcc),
      replyTo: state.replyTo,
      subject: n.subject,
      body: n.body,
      revision: state.revision,
    });
    if (taken !== false) setF(n); // refused while an operation runs
  };
  const hhmm = savedAt?.toLocaleTimeString("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
  const st = saver.current?.status ?? "saved";
  const status =
    st === "saving"
      ? "正在保存…"
      : st === "saved"
        ? `草稿已保存${hhmm ? ` · ${hhmm}` : ""}`
        : "未保存";

  return (
    <main
      aria-label="撰写"
      className="flex min-h-screen flex-col gap-3 p-4"
      onCompositionStart={() => {
        let end = () => {};
        const ended = new Promise<void>((r) => (end = r));
        gate.ime = { ended, end };
        saver.current?.setComposing(true);
      }}
      onCompositionEnd={() => {
        gate.ime?.end();
        gate.ime = undefined;
        saver.current?.setComposing(false);
      }}
    >
      {saver.current?.status === "conflict" && (
        <Alert variant="destructive">草稿已在别处修改，本窗口不再保存</Alert>
      )}
      {(
        [
          ["to", "收件人"],
          ["cc", "抄送"],
          ["bcc", "密送"],
          ["subject", "主题"],
        ] as const
      ).map(([k, label]) => (
        <div key={k} className="flex items-center gap-2">
          <Label htmlFor={k} className="w-16 shrink-0">
            {label}
          </Label>
          <Input id={k} value={f[k]} onChange={set(k)} readOnly={frozen || saver.current?.frozen} />
        </div>
      ))}
      <Label htmlFor="body" className="sr-only">
        正文
      </Label>
      <Textarea
        id="body"
        className="min-h-64 flex-1"
        value={f.body}
        onChange={set("body")}
        readOnly={frozen || saver.current?.frozen}
      />
      {state.quote && (
        <div>
          <Button variant="ghost" size="sm" onClick={() => setShowQuote(!showQuote)}>
            {showQuote ? "隐藏引用的原邮件" : "显示引用的原邮件"}
          </Button>
          {showQuote && (
            <pre className="whitespace-pre-wrap text-sm text-muted-foreground">{state.quote}</pre>
          )}
        </div>
      )}
      <p className="text-sm text-muted-foreground">{status}</p>
    </main>
  );
}
