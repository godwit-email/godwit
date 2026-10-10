import { RiAttachment2 } from "@remixicon/react";
import { currentWindow } from "mygo-runtime";
import { useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Toaster } from "@/components/ui/sonner";
import {
  type Attachment,
  ComposePage as api,
  type ComposeEdit,
  type ComposeState,
  events,
  type SendWarning,
} from "@/mygo";
import { makeT, type T } from "@/shared/i18n";
import { applyTheme, type Theme } from "@/shared/theme";
import { Attachments } from "./Attachments";
import { Editor } from "./editor/Editor";
import { Fields, type FieldsEdit, type FieldsHandle } from "./fields/Fields";
import { Notices } from "./Notices";
import { SendBar } from "./SendBar";
import { createSaver, type Saver } from "./saver";

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
 * The compose page: loads the window's draft, autosaves it, flushes when the window closes; the
 * header fields, the body, attachments, send and discard, and the conflict's resolution.
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
  const [mode, setMode] = useState<Theme["mode"]>(); // a theme event's, once one came
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
    const apply = (t: Theme) => {
      setMode(t.mode);
      return applyTheme(t);
    };
    let stop = apply(state.theme as Theme);
    const off = events.theme.on((t) => {
      stop();
      stop = apply(t as Theme);
    });
    return () => {
      off();
      stop();
    };
  }, [state]);

  if (!state) return <main />;
  return (
    <>
      <Compose state={state} saver={saver} gate={gate.current} frozen={frozen} />
      {/* sonner ignores the html dark class: the theme's mode is its own ("system" follows the OS as applyTheme does) */}
      <Toaster theme={mode ?? (state.theme as Theme).mode} />
    </>
  );
}

// ponytail: matches core's error texts (the bridge passes only err.Error()); typed error codes
// over the bridge would make this exact.
const errorKeys: [string, string][] = [
  ["draft changed since it was loaded", "error.stale_draft"],
  ["attachment exceeds the size limit", "error.too_large"],
];

/** What a rejected call means to the user, in the page's copy. */
export function errorText(e: unknown, t: T): string {
  const msg = e instanceof Error ? e.message : String(e);
  const k = errorKeys.find(([text]) => msg.includes(text));
  return k ? t(k[1]) : t("error.unknown", msg);
}

const fmt = (d: Date) =>
  d.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false });

const editOf = (s: ComposeState): ComposeEdit => ({
  to: s.to,
  cc: s.cc,
  bcc: s.bcc,
  replyTo: s.replyTo,
  subject: s.subject,
  body: s.body,
  revision: s.revision,
});

function Compose({
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
  const t = useMemo(() => makeT(state.text), [state.text]);
  const fieldsRef = useRef<FieldsHandle>(null);
  // The edit as it stands, read synchronously: commitPending's edits arrive within the settling.
  const cur = useRef(editOf(state));
  const [f, setF] = useState(cur.current);
  const [initial, setInitial] = useState({ body: state.body, n: 0 }); // n remounts the editor
  const [from, setFrom] = useState(state.from);
  const [signature, setSignature] = useState(state.signature);
  const [quote, setQuote] = useState(state.quote);
  const [showQuote, setShowQuote] = useState(false);
  const [attachments, setAttachments] = useState<Attachment[]>(state.attachments);
  const [tooLarge, setTooLarge] = useState<string[]>([]);
  const [failed, setFailed] = useState<string[]>([]);
  const [warnings, setWarnings] = useState<{ list: SendWarning[]; digest: string }>({
    list: [],
    digest: "",
  });
  const [blocked, setBlocked] = useState<SendWarning | null>(null);
  const [latestFailed, setLatestFailed] = useState(false);
  const retry = useRef<() => void>(() => {});
  const [sending, setSending] = useState(false);
  const [done, setDone] = useState(false); // sent or discarded: Go closes the window
  const doneRef = useRef(false);
  const [resolving, setResolving] = useState(false); // a conflict resolution runs
  const busy = useRef(false);
  const [savedAt, setSavedAt] = useState(() => fmt(new Date(state.updatedAt)));
  const [, rerender] = useState(0);

  // Created in an effect so StrictMode's discarded mount is disposed, not left with a live timer.
  useEffect(() => {
    const n = createSaver(api.save, { revision: state.revision, snapshot: editOf(state) });
    // The composition's final input lands first: committing before it would keep the half-composed text.
    n.setSettle(async () => {
      const ok = await settle(gate);
      if (ok) fieldsRef.current?.commitPending(); // a composition still open keeps its text pending
      return ok;
    });
    n.onRefused(() => toast(t("compose.sending.locked"), { id: "locked" }));
    let last = n.status;
    const off = n.onChange(() => {
      if (n.status === "saved" && last === "saving") setSavedAt(fmt(new Date()));
      last = n.status;
      rerender((i) => i + 1);
    });
    saver.current = n;
    return () => {
      off();
      n.dispose();
      if (saver.current === n) saver.current = undefined;
    };
  }, [state, saver, gate, t]);
  useEffect(() => {
    currentWindow.setTitle(f.subject.trim() || t("compose.new"));
  }, [f.subject, t]);

  const sv = saver.current;
  const locked = frozen || done || resolving || !!sv?.frozen;
  const finish = () => {
    doneRef.current = true;
    setDone(true);
  };

  const edit = (e: Partial<ComposeEdit>) => {
    // A closing's answer is out (the window may be gone), the draft is sent or discarded, or a
    // conflict resolution is replacing it.
    if (gate.frozen || doneRef.current || busy.current) return;
    const n = { ...cur.current, ...e };
    if (sv?.edit(n) === false) return; // refused while an operation runs
    cur.current = n;
    setF(n);
    setWarnings({ list: [], digest: "" });
    setBlocked(null);
  };
  const failure = (e: unknown) => toast.error(errorText(e, t));

  const send = async (digest = "") => {
    if (!sv || locked || sending) return;
    setSending(true);
    try {
      const r = await sv.run((rev) => api.send(rev, digest).then((s) => ({ ...s, revision: rev })));
      if (!r) return; // not saved: the status says why
      if (r.sent) finish();
      setWarnings({ list: r.warnings ?? [], digest: r.digest });
      setBlocked(r.blocked);
    } catch (e) {
      failure(e);
    } finally {
      setSending(false);
    }
  };
  const onFrom = async (id: number) => {
    if (!sv || id === from) return;
    const prev = from;
    setFrom(id);
    try {
      const r = await sv.run((rev) => api.setAccount(id, rev));
      if (!r || r.conflict) setFrom(prev);
      else {
        setSignature(r.signature);
        setSavedAt(fmt(new Date(r.updatedAt)));
      }
    } catch (e) {
      setFrom(prev);
      failure(e);
    }
  };
  const change = async (op: (rev: number) => ReturnType<typeof api.addAttachments>) => {
    if (!sv) return;
    try {
      const r = await sv.run(op);
      if (!r) return;
      // A conflict that stopped the adding part way still lists the files added (with a revision).
      if (!r.conflict || r.revision) {
        setAttachments(r.attachments);
        setSavedAt(fmt(new Date(r.updatedAt)));
      }
      setTooLarge(r.tooLarge ?? []);
      setFailed(r.failed ?? []);
    } catch (e) {
      failure(e);
    }
  };
  const discard = async () => {
    if (!sv) return;
    try {
      const r = await sv.run(async (rev) => {
        await api.discard();
        return { revision: rev, conflict: false };
      });
      if (r) finish();
    } catch (e) {
      failure(e);
    }
  };
  // The conflict: read the draft as stored; keep mine saves the local edit over it, use latest
  // takes it. Either way its account, signature, quote and attachments (not edited here) are adopted.
  // One at a time, the form frozen throughout; a reply for a saver since replaced is dropped.
  const resolve = async (mine: boolean) => {
    if (!sv || busy.current) return;
    busy.current = true;
    setResolving(true);
    retry.current = () => resolve(mine);
    try {
      let l: ComposeState;
      try {
        l = await api.latest();
      } catch {
        setLatestFailed(true);
        return;
      }
      if (saver.current !== sv) return;
      setLatestFailed(false);
      setFrom(l.from);
      setSignature(l.signature);
      setQuote(l.quote);
      setAttachments(l.attachments);
      if (mine) {
        await sv.keepMine(l.revision);
        return;
      }
      cur.current = editOf(l);
      setF(cur.current);
      setInitial((i) => ({ body: l.body, n: i.n + 1 })); // remounts the editor and the fields
      setSavedAt(fmt(new Date(l.updatedAt)));
      // biome-ignore lint/correctness/useHookAtTopLevel: the saver's method, not a hook
      sv.useLatest(l.revision, cur.current);
    } finally {
      busy.current = false;
      setResolving(false);
    }
  };

  const status = sv?.status === "saving" || sv?.status === "saved" ? sv.status : "unsaved";
  const title = f.subject.trim() || t("compose.new");

  return (
    <main
      aria-label={title}
      className="flex h-screen flex-col"
      onCompositionStart={() => {
        let end = () => {};
        const ended = new Promise<void>((r) => (end = r));
        gate.ime = { ended, end };
        sv?.setComposing(true);
      }}
      onCompositionEnd={() => {
        gate.ime?.end();
        gate.ime = undefined;
        sv?.setComposing(false);
      }}
      onKeyDown={(e) => {
        // The editor is read-only while an operation runs, so typing there never reaches the saver.
        if (
          (sv?.frozen || resolving) &&
          !gate.frozen &&
          !done &&
          e.key.length === 1 &&
          !e.metaKey &&
          !e.ctrlKey
        )
          toast(t("compose.sending.locked"), { id: "locked" });
        if (e.key !== "Enter" || !(e.metaKey || e.ctrlKey) || e.nativeEvent.isComposing) return;
        e.preventDefault();
        void send();
      }}
    >
      <header className="compose-titlebar flex shrink-0 items-center border-b pr-4">
        <div className="flex min-w-0 items-baseline gap-2">
          <h1 className="truncate font-semibold text-md">{title}</h1>
          <p className="shrink-0 text-muted-foreground text-sm">
            {status === "saved"
              ? t("compose.status.saved", savedAt)
              : t(`compose.status.${status}`)}
          </p>
        </div>
      </header>
      {/* A row: the compose column now, a side panel (Wit, plan 3) beside it later. */}
      <div className="flex min-h-0 flex-1">
        <section className="flex min-w-0 flex-1 flex-col">
          <div className="flex min-h-0 flex-1 flex-col overflow-auto">
            <Fields
              key={initial.n}
              ref={fieldsRef}
              frozen={locked}
              accounts={state.accounts}
              from={from}
              onFrom={onFrom}
              to={f.to}
              cc={f.cc}
              bcc={f.bcc}
              replyTo={f.replyTo}
              subject={f.subject}
              onChange={(e: FieldsEdit) => edit(e)}
              t={t}
            />
            <Editor
              key={`${state.draftId}:${initial.n}`}
              initial={initial.body}
              readOnly={locked}
              placeholder=""
              onChange={(body) => edit({ body })}
              onComposing={() => {}} // <main> tracks compositions, the fields' too
              t={t}
              attach={
                <Button
                  variant="ghost"
                  size="icon-sm"
                  className="size-7.5 rounded-md text-foreground/80"
                  aria-label={t("compose.attach")}
                  disabled={locked}
                  onClick={() => change((rev) => api.addAttachments(rev))}
                >
                  <RiAttachment2 className="size-3.75" />
                </Button>
              }
            />
            <div className="flex flex-col gap-4 px-5 pb-4 *:empty:hidden">
              {signature && (
                <div className="whitespace-pre-wrap text-base text-muted-foreground">
                  {signature}
                </div>
              )}
              {quote && (
                <div className="text-base text-muted-foreground">
                  <Button
                    variant="link"
                    size="sm"
                    className="h-auto px-0 font-normal text-base text-muted-foreground hover:text-foreground"
                    onClick={() => setShowQuote(!showQuote)}
                  >
                    {t("compose.quote.show")}
                  </Button>
                  {showQuote && (
                    <div className="mt-2 whitespace-pre-wrap border-l-2 pl-3">{quote}</div>
                  )}
                </div>
              )}
              <Attachments
                items={attachments}
                tooLarge={tooLarge}
                failed={failed}
                frozen={locked}
                onRemove={(id) =>
                  change((rev) =>
                    api.removeAttachment(id, rev).then((c) => ({ ...c, tooLarge: [], failed: [] })),
                  )
                }
                t={t}
              />
              <Notices
                warnings={warnings.list}
                blocked={blocked}
                conflict={sv?.status === "conflict"}
                latestFailed={latestFailed}
                frozen={locked}
                onSendAnyway={() => send(warnings.digest)}
                onKeepMine={() => resolve(true)}
                onUseLatest={() => resolve(false)}
                onRetry={() => retry.current()}
                t={t}
              />
            </div>
          </div>
          <div className="shrink-0 border-t px-5 py-3">
            <SendBar
              sending={sending}
              frozen={locked}
              onSend={() => send()}
              onDiscard={discard}
              t={t}
            />
          </div>
        </section>
      </div>
    </main>
  );
}
