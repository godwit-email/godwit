import { RiAttachment2, RiPlayFill, RiSparkling2Line } from "@remixicon/react";
import { cn } from "cn";
import { currentWindow } from "mygo-runtime";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useDefaultLayout } from "react-resizable-panels";
import { toast } from "sonner";
import { Alert, AlertAction, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable";
import { Toaster } from "@/components/ui/sonner";
import {
  type Approval,
  type Attachment,
  ComposePage as api,
  type BodyChange,
  type ComposeEdit,
  type ComposeState,
  events,
  type Review as ReviewT,
  type SendWarning,
  type WitEvent,
  type WitHistory,
} from "@/mygo";
import { type T, useT } from "@/shared/i18n";
import { applyTheme, type Theme } from "@/shared/theme";
import { Panel as WitPanel, witError } from "@/wit/Panel";
import { useResume } from "@/wit/resume";
import { ends, WitTransport } from "@/wit/transport";
import { Attachments } from "./Attachments";
import { type CmdKSelection, Editor } from "./editor/Editor";
import { IdleToolbar } from "./editor/Toolbar";
import { Fields, type FieldsEdit, type FieldsHandle } from "./fields/Fields";
import { Notices } from "./Notices";
import { CmdK } from "./review/CmdK";
import { BODY, Review, reviewSummary, Static } from "./review/Review";
import { SendBar } from "./SendBar";
import { createSaver, type Saver } from "./saver";

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));
/** The window shows the main window's material (macOS): the page tints it rather than covers it. */
const material = new URLSearchParams(location.search).has("material");

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
 * header fields, the body, attachments, send, and the conflict's resolution.
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
  ["link reference that could not be restored", "error.link_ref"],
];

/** What a rejected call means to the user, in the page's copy. */
export function errorText(e: unknown, t: T): string {
  const msg = e instanceof Error ? e.message : String(e);
  const k = errorKeys.find(([text]) => msg.includes(text));
  return k ? t(k[1]) : t("error.unknown", msg);
}

/** A draft edit waiting for a decision: the page reviews it in place of the body. */
const pendingEdit = (a: Approval) =>
  a.state === "pending" && (a.tool === "update_draft" || a.tool === "update_selection");

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

/** update_draft's HTML from its call's JSON arguments; "" for none. */
function draftHTML(args: string): string {
  try {
    const h = JSON.parse(args)?.html;
    return typeof h === "string" ? h : "";
  } catch {
    return "";
  }
}

const fmt = (d: Date) =>
  `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;

/** The read-only signature: Go's sanitized HTML (core.SignatureShown), "" for none. */
const sigOf = (s: { signatureHtml: string }) => s.signatureHtml;

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
  const t = useT(state.locale);
  const currentT = useRef(t);
  currentT.current = t;
  const fieldsRef = useRef<FieldsHandle>(null);
  // The edit as it stands, read synchronously: commitPending's edits arrive within the settling.
  const cur = useRef(editOf(state));
  const [f, setF] = useState(cur.current);
  const [initial, setInitial] = useState({ body: state.body, n: 0 }); // n remounts the editor
  const [fieldsKey, setFieldsKey] = useState(0); // remounts the fields
  const [from, setFrom] = useState(state.from);
  const [signature, setSignature] = useState(sigOf(state));
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
  const [done, setDone] = useState(false); // sent: Go closes the window
  const doneRef = useRef(false);
  const [resolving, setResolving] = useState(false); // a conflict resolution runs
  const busy = useRef(false);
  const [savedAt, setSavedAt] = useState(() => fmt(new Date(state.updatedAt)));
  const [, rerender] = useState(0);
  const refresh = useRef(false); // a reload skipped while the saver was busy: retried once idle
  const reloadRef = useRef(async () => {});

  // Created in an effect so StrictMode's discarded mount is disposed, not left with a live timer; a
  // layout one, re-rendered at once, so no input reaches the editor before the saver (an edit then is lost).
  useLayoutEffect(() => {
    const n = createSaver(api.save, { revision: state.revision, snapshot: editOf(state) });
    // The composition's final input lands first: committing before it would keep the half-composed text.
    n.setSettle(async () => {
      const ok = await settle(gate);
      if (ok) fieldsRef.current?.commitPending(); // a composition still open keeps its text pending
      return ok;
    });
    n.onRefused(() => lockToast.current("compose.sending.locked"));
    let last = n.status;
    const off = n.onChange(() => {
      if (n.status === "saved" && last === "saving") setSavedAt(fmt(new Date()));
      last = n.status;
      if (!n.busy && refresh.current) {
        refresh.current = false;
        void reloadRef.current(); // a reload skipped while busy: read again, checked again
      }
      rerender((i) => i + 1);
    });
    saver.current = n;
    rerender((i) => i + 1); // the handlers read the saver from the render
    return () => {
      off();
      n.dispose();
      if (saver.current === n) saver.current = undefined;
    };
  }, [state, saver, gate]);
  useEffect(() => {
    currentWindow.setTitle(f.subject.trim() || t("compose.new"));
  }, [f.subject, t]);

  // Wit, when a model is configured. The page tracks what locks the body: a run streaming in the
  // panel or a ⌘K run, an edit waiting (reviewed in place of the body), a reload in flight.
  const [wit, setWit] = useState<{ tr: WitTransport; history: WitHistory; n: number }>();
  // The conversation's model and reasoning effort, as the next question asks on them (the Wit
  // page's chip): the history's, until one is picked.
  const pick = useRef({ model: "", effort: "" });
  const [modelRef, setModelRef] = useState("");
  const [effort, setEffort] = useState("");
  const history = wit?.history;
  useEffect(() => {
    if (!history) return;
    pick.current = { model: history.model, effort: history.effort };
    setModelRef(history.model);
    setEffort(history.effort);
  }, [history]);
  const [panelRunning, setPanelRunning] = useState(false);
  const [cmdkRun, setCmdkRun] = useState(0); // -1 while it starts
  const [pendings, setPendings] = useState<Approval[]>([]);
  const [review, setReview] = useState<ReviewT>();
  const [deciding, setDeciding] = useState(false);
  const [reloading, setReloading] = useState(false);
  const [reloadErr, setReloadErr] = useState(""); // a failed reload's error, in the page's copy
  const [applied, setApplied] = useState(false);
  const [cmdk, setCmdk] = useState<CmdKSelection | null>(null);
  const [streamed, setStreamed] = useState(""); // update_draft's HTML, shown while Wit writes a blank body
  const edits = useRef(0); // local body edits taken
  const runEdits = useRef(0); // body edits when the last run or decision started
  const reloads = useRef(0);
  const cmdkRef = useRef(0);
  const cmdkStop = useRef(false); // Stop asked for while the ⌘K run starts
  const ended = useRef(new Set<number>()); // runs seen ending
  const trRef = useRef<WitTransport>(undefined);
  const witResume = useResume(wit?.tr);
  // The Wit panel's width as last dragged, kept per layout (with and without the panel).
  const layout = useDefaultLayout({
    id: "compose",
    panelIds: wit ? ["editor", "wit"] : ["editor"],
  });
  const toasted = useRef(false); // the lock's toast, once per lock
  const bodyRef = useRef<HTMLDivElement>(null);
  const [toolbarSlot, setToolbarSlot] = useState<HTMLDivElement | null>(null);
  // The one way a locked page says so: once until it unlocks.
  const lockToast = useRef<(key: string) => void>(() => {});
  lockToast.current = (key) => {
    if (toasted.current) return;
    toasted.current = true;
    (key === "wit.compose.locked" ? toast.warning : toast)(t(key), { id: "locked" });
  };

  // The body as stored, adopted: the editor remounts on it; the saver reads it as saved.
  const adopt = (revision: number, body: string, subject?: string) => {
    cur.current = {
      ...cur.current,
      body,
      revision,
      ...(subject !== undefined ? { subject } : {}),
    };
    setF(cur.current);
    setInitial((i) => ({ body, n: i.n + 1 }));
    // biome-ignore lint/correctness/useHookAtTopLevel: the saver's method, not a hook
    saver.current?.useLatest(revision, cur.current);
  };

  // Reads the draft after a run or a decision; the newest read wins. Skipped in conflict, when the
  // stored revision is not newer, or with a local edit since the run; while the saver is busy,
  // read again once it is idle.
  const reload = async () => {
    const n = ++reloads.current;
    setReloading(true);
    setReloadErr("");
    try {
      const l = await api.latest();
      const s = saver.current;
      if (n !== reloads.current || !s) return;
      if (s.busy) {
        refresh.current = true;
        return;
      }
      if (s.status === "conflict" || s.dirty || l.revision <= s.revision) return;
      if (edits.current !== runEdits.current) return;
      adopt(l.revision, l.body, l.subject);
      setApplied(true);
    } catch (e) {
      if (n === reloads.current) setReloadErr(witError(message(e), t));
    } finally {
      // A run's end reloads: its streamed text goes (adopted or not), so no later run shows it.
      if (n === reloads.current) {
        setReloading(false);
        setStreamed("");
      }
    }
  };

  reloadRef.current = reload;

  // The panel follows the draft's conversation again (after ⌘K: the one its edit waits in).
  const refollow = () => {
    const tr = trRef.current;
    if (!tr) return;
    tr.follow().then(
      (history) => {
        if (!history) return; // a newer follow's
        setPendings(history.approvals.filter(pendingEdit));
        witResume.reset(); // the remounted panel must not resume an old run
        setWit((w) => w && { tr, history, n: w.n + 1 });
      },
      (e) => toast.error(witError(message(e), t)),
    );
  };

  const onWit = useRef<(ev: WitEvent) => void>(() => {});
  onWit.current = (ev) => {
    if (ev.kind === "tool_call" && ev.call?.name === "update_draft")
      setStreamed(draftHTML(ev.call.args));
    if (ev.kind === "interrupted") setPendings(ev.approvals.filter(pendingEdit));
    if (!ends(ev)) return;
    // Over: none of its body edits waits any more (an interrupt's are set above).
    if (ev.kind !== "interrupted") setPendings((p) => p.filter((x) => x.run !== ev.run));
    ended.current.add(ev.run);
    if (ev.run === cmdkRef.current) {
      cmdkRef.current = 0;
      setCmdkRun(0);
      refollow();
    }
    void reload();
  };

  // A run starts only once the edit is saved (the saver's queue); nothing runs if that fails.
  const startRun = useRef(async (start: () => Promise<number>) => {
    const s = saver.current;
    setApplied(false);
    setStreamed("");
    const r = await s?.run(async (rev) => {
      runEdits.current = edits.current;
      return { revision: rev, conflict: false, run: await start() };
    });
    if (!r) throw new Error(""); // not saved: the status says why; the panel shows no error
    return r.run;
  });

  // The page listens, then the transport, before follow reads.
  useEffect(() => {
    if (!state.wit.enabled) return;
    const off = events.wit.on((ev) => onWit.current(ev));
    const tr = new WitTransport({
      ask: (text, quoted = []) =>
        startRun.current(() => api.ask(text, quoted, pick.current.model, pick.current.effort)),
      skill: (id, lang) => startRun.current(() => api.skill(id, lang)),
      follow: () => api.follow(),
      stop: (run) => api.stop(run),
    });
    trRef.current = tr;
    let live = true;
    tr.follow().then(
      (history) => {
        if (!live || !history) return;
        setPendings(history.approvals.filter(pendingEdit));
        setWit({ tr, history, n: 0 });
      },
      (e) => live && toast.error(witError(message(e), currentT.current)),
    );
    return () => {
      live = false;
      off();
      tr.close();
    };
  }, [state]);

  const current = pendings[0];
  const currentId = current?.id;
  useEffect(() => {
    setReview(undefined);
    if (!currentId) return;
    let live = true;
    api.review(currentId).then(
      (r) => live && setReview(r),
      (e) => live && toast.error(witError(message(e), currentT.current)),
    );
    return () => {
      live = false;
    };
  }, [currentId]);

  const reviewing = review && review.approval === currentId ? review : undefined;

  const sv = saver.current;
  const locked = frozen || done || resolving || !!sv?.frozen;
  const running = panelRunning || cmdkRun !== 0;
  const witLocked = running || !!current || reloading || sending;
  const anyLocked = witLocked || resolving || !!sv?.frozen;
  useEffect(() => {
    if (!anyLocked) toasted.current = false;
  }, [anyLocked]);
  const finish = () => {
    doneRef.current = true;
    setDone(true);
  };

  const edit = (e: Partial<ComposeEdit>) => {
    // A closing's answer is out (the window may be gone), the draft is sent, or a
    // conflict resolution is replacing it.
    if (gate.frozen || doneRef.current || busy.current) return;
    const n = { ...cur.current, ...e };
    if (sv?.edit(n) === false) return; // refused while an operation runs
    cur.current = n;
    setF(n);
    // Only the body counts for Wit: a reload skips a body edited since the run; fields don't block it.
    if (e.body !== undefined) {
      edits.current++;
      setApplied(false);
    }
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
        setSignature(sigOf(r));
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
  // A body operation (a decision, undo) in the saver's queue; its BodyChange is adopted at once.
  const bodyOp = async (op: () => Promise<BodyChange>) => {
    if (!sv) return null;
    setDeciding(true);
    try {
      const r = await sv.run(op);
      if (r && !r.conflict) {
        adopt(r.revision, r.body, r.subject);
        setSignature(sigOf(r));
        setAttachments(r.attachments);
        setSavedAt(fmt(new Date(r.updatedAt)));
      }
      return r;
    } catch (e) {
      toast.error(witError(message(e), t));
      return null;
    } finally {
      setDeciding(false);
    }
  };
  // Decides the edit reviewed; the run it resumes streams in the panel, and the draft is read again.
  const decide = async (op: (id: number) => Promise<BodyChange>, accepts: boolean) => {
    const a = current;
    if (!a || deciding) return;
    runEdits.current = edits.current;
    const r = await bodyOp(() => witResume.decide(a.run, () => op(a.id)));
    if (r) setPendings((p) => p.filter((x) => x.id !== a.id));
    if (r && !r.conflict && accepts) setApplied(true); // the body took Wit's edit: undo offers it back
    void reload();
  };
  // Decides a tool's call (its card in the panel), or every one of its run waiting; the run it
  // resumes streams in the panel.
  const decideTool = async (a: Approval, accept: boolean, all: boolean) => {
    if (deciding) return false;
    runEdits.current = edits.current;
    const r = await bodyOp(() =>
      witResume.decide(a.run, () =>
        all ? api.decideAll(a.run, accept) : api.decide(a.id, accept),
      ),
    );
    if (r && all) {
      // The run's body edits were decided with it: no longer reviewed; an accepted one applied.
      const edited = pendings.some((x) => x.run === a.run);
      setPendings((p) => p.filter((x) => x.run !== a.run));
      if (edited && accept && !r.conflict) setApplied(true);
    }
    return !!r;
  };
  const undo = async () => {
    setApplied(false);
    await bodyOp(() => api.undo());
  };
  const closeCmdK = useCallback(() => setCmdk(null), []);
  const openCmdK = (sel: CmdKSelection) => {
    setApplied(false); // ⌘K clears it, as a run or an edit does
    setCmdk(sel);
  };
  // A ⌘K run still starting is stopped as soon as Rewrite returns its ID.
  const stopRun = (run: number) =>
    void api.stop(run).catch((e) => toast.error(witError(message(e), t)));
  const stop = () => {
    if (cmdkRun === -1) cmdkStop.current = true;
    else if (cmdkRun > 0) stopRun(cmdkRun);
    else wit?.tr.stop().catch((e) => toast.error(witError(message(e), t)));
  };
  // ⌘K: saved first, then the selection of the text as saved is rewritten (a run of its own).
  const rewrite = async (sel: CmdKSelection, instruction: string) => {
    setCmdk(null);
    setApplied(false);
    if (!sv) return;
    cmdkRef.current = -1;
    cmdkStop.current = false;
    setCmdkRun(-1);
    try {
      const r = await sv.run((rev) => {
        runEdits.current = edits.current;
        return api.rewrite(rev, sel.text, sel.start, sel.end, instruction);
      });
      if (r && !r.conflict && !ended.current.has(r.run)) {
        cmdkRef.current = r.run;
        setCmdkRun(r.run);
        if (cmdkStop.current) stopRun(r.run);
        return;
      }
      if (r && !r.conflict) refollow(); // over already
    } catch (e) {
      toast.error(witError(message(e), t));
    } finally {
      cmdkStop.current = false;
    }
    cmdkRef.current = 0;
    setCmdkRun(0);
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
      setSignature(sigOf(l));
      setQuote(l.quote);
      setAttachments(l.attachments);
      if (mine) {
        await sv.keepMine(l.revision);
        return;
      }
      cur.current = editOf(l);
      setF(cur.current);
      setInitial((i) => ({ body: l.body, n: i.n + 1 })); // remounts the editor
      setFieldsKey((k) => k + 1);
      setSavedAt(fmt(new Date(l.updatedAt)));
      // biome-ignore lint/correctness/useHookAtTopLevel: the saver's method, not a hook
      sv.useLatest(l.revision, cur.current);
    } finally {
      busy.current = false;
      setResolving(false);
    }
  };

  const status = sv?.status === "saving" || sv?.status === "saved" ? sv.status : "unsaved";
  // Wit writes a blank body: its text shows read-only until the draft is read again.
  const writing = (running || reloading) && !f.body.replace(/<[^>]*>|&nbsp;/g, "").trim();
  const showEditor = !reviewing && !writing;
  const attach = (
    <Button
      variant="ghost"
      size="icon-sm"
      className="size-7 rounded-md text-foreground/80 disabled:opacity-100"
      aria-label={t("compose.attach")}
      disabled={locked || witLocked} // an attachment bumps the revision: a waiting edit would go stale
      onClick={() => change((rev) => api.addAttachments(rev))}
    >
      <RiAttachment2 className="size-3.75" />
    </Button>
  );
  const title = f.subject.trim() || t("compose.new");
  // After the body's text: the signature, then the quoted mail behind its toggle.
  const tail = (
    <>
      {signature && (
        <div
          data-slot="signature-preview"
          className="text-base text-muted-foreground [&_a]:underline [&_img]:max-w-full"
          // biome-ignore lint/security/noDangerouslySetInnerHtml: Go sanitized it (SanitizeDraft) before inlining its images
          dangerouslySetInnerHTML={{ __html: signature }}
        />
      )}
      {quote && (
        <div className="mt-2 text-base text-muted-foreground">
          <Button
            variant="link"
            size="sm"
            aria-expanded={showQuote}
            className="h-auto gap-1.25 px-0 font-normal text-base text-muted-foreground hover:text-foreground hover:no-underline"
            onClick={() => setShowQuote(!showQuote)}
          >
            <RiPlayFill
              className={`-ml-1.25 size-3.5 transition-transform ${showQuote ? "rotate-90" : ""}`}
            />
            {t("compose.quote.show")}
          </Button>
          {showQuote && <div className="mt-2 whitespace-pre-wrap border-l-2 pl-3">{quote}</div>}
        </div>
      )}
    </>
  );

  return (
    <main
      aria-label={title}
      className={cn("flex h-screen flex-col", material ? "bg-window-material" : "bg-window")}
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
        // Typing into a locked page says why, once: an operation (sending) anywhere; Wit's lock in
        // the body (not on the review's own buttons). Read-only inputs never reach the saver.
        const target = e.target as HTMLElement;
        if (e.key.length === 1 && !e.metaKey && !e.ctrlKey && !gate.frozen && !done) {
          if (sv?.frozen || resolving || sending) lockToast.current("compose.sending.locked");
          else if (witLocked && bodyRef.current?.contains(target) && !target.closest("button"))
            lockToast.current("wit.compose.locked");
        }
        if (e.key !== "Enter" || !(e.metaKey || e.ctrlKey) || e.nativeEvent.isComposing) return;
        e.preventDefault();
        void send();
      }}
    >
      <header className="compose-titlebar flex shrink-0 items-center gap-2 pr-4">
        <h1 className="min-w-0 truncate font-semibold text-md">{title}</h1>
        <p className="shrink-0 text-muted-foreground text-sm">
          {status === "saved" ? t("compose.status.saved", savedAt) : t(`compose.status.${status}`)}
        </p>
      </header>
      {/* A row of cards on the window's material: the compose column, the Wit panel beside it,
          the gap between them a grip that sets the panel's width. */}
      <ResizablePanelGroup
        orientation="horizontal"
        className="min-h-0 flex-1 px-2 pb-2"
        defaultLayout={layout.defaultLayout}
        onLayoutChanged={layout.onLayoutChanged}
      >
        <ResizablePanel id="editor" minSize={320}>
          <section className="flex h-full min-w-0 flex-col overflow-hidden rounded-xl border-[0.5px] bg-background shadow-(--shadow-xs)">
            <div className="flex min-h-0 flex-1 flex-col overflow-auto">
              <Fields
                key={fieldsKey}
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
              {/* The body: locked while Wit writes or an edit waits (typing there toasts why, once). */}
              <div ref={bodyRef} className="flex flex-col">
                {reloadErr && (
                  <Alert
                    variant="destructive"
                    className="mx-4 mt-2 w-auto border-0 bg-destructive/10"
                  >
                    <AlertDescription>{reloadErr}</AlertDescription>
                    <AlertAction>
                      <Button variant="ghost" size="sm" onClick={() => void reload()}>
                        {t("common.retry")}
                      </Button>
                    </AlertAction>
                  </Alert>
                )}
                {running && (
                  <div className="flex items-center gap-2 bg-sidebar px-4 py-2 text-base">
                    <RiSparkling2Line className="size-3.5 shrink-0" />
                    <span className="flex-1">{t("wit.compose.writing_banner")}</span>
                    <Button variant="outline" size="sm" className="text-base" onClick={stop}>
                      {t("wit.stop")}
                    </Button>
                  </div>
                )}
                {applied && !witLocked && !locked && (
                  <div className="mx-4 mt-2 flex items-center gap-2 text-muted-foreground text-sm">
                    <span>{t("wit.compose.applied")}</span>
                    <Button variant="link" size="sm" className="h-auto px-0" onClick={undo}>
                      {t("wit.compose.undo")}
                    </Button>
                  </div>
                )}
                {reviewing ? (
                  <Review
                    key={reviewing.approval}
                    review={reviewing}
                    busy={deciding}
                    onDecide={(accept) => decide((id) => api.decide(id, accept), accept)}
                    onFinish={(ds) => decide((id) => api.finish(id, ds), ds.includes(1))}
                    after={tail}
                    t={t}
                  />
                ) : (
                  // biome-ignore lint/a11y/noStaticElementInteractions: a click below the text only moves focus into it
                  <div
                    data-locked={running || undefined}
                    className={`${BODY} data-locked:bg-sidebar`}
                    onMouseDown={(e) => {
                      if (e.target !== e.currentTarget) return;
                      e.preventDefault();
                      e.currentTarget.querySelector<HTMLElement>("[contenteditable=true]")?.focus();
                    }}
                  >
                    {writing ? (
                      <Static html={streamed} label={t("compose.body")} caret />
                    ) : (
                      <Editor
                        key={`${state.draftId}:${initial.n}`}
                        initial={initial.body}
                        readOnly={locked || witLocked}
                        placeholder={wit && !state.wit.translateOnly ? t("wit.compose.empty") : ""}
                        onChange={(body) => edit({ body })}
                        onComposing={() => {}} // <main> tracks compositions, the fields' too
                        t={t}
                        onCmdK={
                          wit && !locked && !witLocked && !state.wit.translateOnly
                            ? openCmdK
                            : undefined
                        }
                        toolbarSlot={toolbarSlot}
                        attach={attach}
                        popover={
                          cmdk &&
                          !locked &&
                          !witLocked && (
                            <CmdK
                              onSubmit={(instruction) => void rewrite(cmdk, instruction)}
                              onClose={closeCmdK}
                              t={t}
                            />
                          )
                        }
                      />
                    )}
                    {tail}
                  </div>
                )}
              </div>
              <div className="mx-4 mb-4 flex flex-col gap-4 *:empty:hidden">
                <Attachments
                  items={attachments}
                  tooLarge={tooLarge}
                  failed={failed}
                  frozen={locked}
                  onRemove={(id) =>
                    change((rev) =>
                      api
                        .removeAttachment(id, rev)
                        .then((c) => ({ ...c, tooLarge: [], failed: [] })),
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
            {/* The toolbar (rendered by the editor into the slot, or idle while none shows), then Send. */}
            <div data-bar className="mx-4 flex shrink-0 flex-wrap items-center gap-2 py-3">
              <div ref={setToolbarSlot} className="contents">
                {!showEditor && <IdleToolbar t={t} attach={attach} />}
              </div>
              <SendBar sending={sending} frozen={locked} onSend={() => send()} t={t} />
            </div>
          </section>
        </ResizablePanel>
        {wit && <ResizableHandle label={t("shell.resize")} />}
        {wit && (
          <ResizablePanel id="wit" defaultSize={320} minSize={280} maxSize="50%">
            <WitPanel
              info={state.wit}
              history={wit.history}
              key={wit.n}
              locked={locked || witLocked}
              review={
                reviewing
                  ? {
                      approval: reviewing.approval,
                      summary: reviewSummary(reviewing, t),
                      actions: (
                        <>
                          <Button
                            size="sm"
                            className="px-3 font-normal text-base"
                            disabled={deciding}
                            onClick={() => decide((id) => api.decide(id, true), true)}
                          >
                            {t("wit.compose.accept_all")}
                          </Button>
                          <Button
                            size="sm"
                            variant="outline"
                            className="font-normal text-base"
                            disabled={deciding}
                            onClick={() => decide((id) => api.decide(id, false), false)}
                          >
                            {t("wit.compose.reject_all")}
                          </Button>
                        </>
                      ),
                    }
                  : undefined
              }
              resume={witResume.resume}
              onLink={(url) => api.openLink(url)}
              onCite={(id) =>
                api.openCited(id).catch((e) => {
                  toast.error(witError(message(e), t));
                  throw e;
                })
              }
              onCheckCited={(id) =>
                api.checkCited(id).catch((e) => {
                  toast.error(witError(message(e), t));
                  throw e;
                })
              }
              models={{
                ref: modelRef,
                list: () => api.models(),
                pick: (r) => {
                  pick.current.model = r;
                  setModelRef(r);
                },
              }}
              effort={{
                value: effort,
                pick: (e) => {
                  pick.current.effort = e;
                  setEffort(e);
                },
              }}
              onRunning={setPanelRunning}
              onDecide={decideTool}
              onSearch={(q) => api.searchMail(q)}
              model={state.wit.model}
              onModels={() =>
                void api.openModels().catch((e) => toast.error(witError(message(e), t)))
              }
              transport={wit.tr}
              t={t}
            />
          </ResizablePanel>
        )}
      </ResizablePanelGroup>
    </main>
  );
}
