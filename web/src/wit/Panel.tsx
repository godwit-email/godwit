import { useChat } from "@ai-sdk/react";
import {
  RiAddLine,
  RiAlertLine,
  RiArrowDownLine,
  RiArrowDownSLine,
  RiArrowUpLine,
  RiBrainLine,
  RiCloseLine,
  RiEditLine,
  RiEqualizerLine,
  RiFileCopyLine,
  RiLoaderLine,
  RiMailLine,
  RiPieChart2Line,
  RiRefreshLine,
  RiSettings3Line,
  RiStopFill,
  RiToolsLine,
  RiTranslate2,
} from "@remixicon/react";
import type { DynamicToolUIPart, ReasoningUIPart } from "ai";
import { cn } from "cn";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { Alert, AlertAction, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Bubble, BubbleContent } from "@/components/ui/bubble";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { HoverCard, HoverCardContent, HoverCardTrigger } from "@/components/ui/hover-card";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupTextarea,
} from "@/components/ui/input-group";
import { Message, MessageContent } from "@/components/ui/message";
import {
  MessageScroller,
  MessageScrollerButton,
  MessageScrollerContent,
  MessageScrollerItem,
  MessageScrollerProvider,
  MessageScrollerViewport,
} from "@/components/ui/message-scroller";
import { Progress } from "@/components/ui/progress";
import {
  Trace,
  TraceCollapsible,
  TraceCollapsibleContent,
  TraceCollapsibleTrigger,
  TraceIcon,
  TraceLine,
  TraceTitle,
} from "@/components/ui/trace";
import type { Approval, Citation, MailHit, ModelChoice, WitHistory, WitInfo } from "@/mygo";
import type { T } from "@/shared/i18n";
import { Citations, type Nums } from "./Citations";
import { type PickerKeys, QuoteChips, QuotePicker } from "./Quote";
import { Response } from "./Response";
import { historyToMessages, type WitMeta, type WitTransport, type WitUIMessage } from "./transport";

/** The emails a question quotes at most (core maxQuoted). */
const MAX_QUOTED = 10;

// ponytail: shadcn's spinner.tsx fails the typecheck (children on a Remix icon); its markup here.
const Busy = ({ t }: { t: T }) => (
  <RiLoaderLine
    role="status"
    aria-label={t("wit.loading")}
    className="size-4 animate-spin text-muted-foreground"
  />
);

const ARGS = 160; // a call's arguments shown, in characters (app/wit.go argsRunes)
const BODY = 240; // a call's body on its approval card (app/wit.go bodyRunes)

/** An error key (or a bridge error's text) in the page's copy. */
export function witError(m: string, t: T): string {
  if (m === "error.unknown") return t(m, "");
  return t(m) !== m ? t(m) : t("error.unknown", m);
}

/** Each message the parts of ms cite, once; suspect if any citation of it is. */
function citesOf(...ms: WitUIMessage[]): Citation[] {
  const out: Citation[] = [];
  for (const p of ms.flatMap((m) => m.parts)) {
    if (p.type !== "data-citations") continue;
    for (const c of p.data) {
      const i = out.findIndex((x) => x.messageId === c.messageId);
      if (i < 0) out.push({ ...c });
      else {
        out[i].suspect ||= c.suspect;
        out[i].quote ||= c.quote;
      }
    }
  }
  return out;
}

/**
 * The tools an answer called: on the page "调用了 n 个工具 · a ×2、b" (the distinct tools, each
 * with its count), in the panel "调用了 a · b".
 */
function toolsLabel(calls: DynamicToolUIPart[], page: boolean, t: T): string {
  const count = new Map<string, number>();
  for (const c of calls) count.set(c.toolName, (count.get(c.toolName) ?? 0) + 1);
  if (!page) return t("wit.tools.called", [...count.keys()].join(" · "));
  const names = [...count].map(([n, k]) => (k > 1 ? `${n} ×${k}` : n));
  return `${t("wit.tools", count.size)} · ${names.join(t("mail.list_sep"))}`;
}

const clip = (s: string, n = ARGS) => (s.length > n ? `${s.slice(0, n)}…` : s);

const isDraftEdit = (tool: string) => tool === "update_draft" || tool === "update_selection";

const toolLabel = (tool: string, t: T) =>
  t(`wit.tool.${tool}`) !== `wit.tool.${tool}` ? t(`wit.tool.${tool}`) : tool;

const strList = (v: unknown): string[] =>
  typeof v === "string" ? [v] : Array.isArray(v) ? v.filter((x) => typeof x === "string") : [];

/** Decides approval a, or with all every pending one of its run; true once decided. */
export type Decide = (a: Approval, accept: boolean, all: boolean) => Promise<boolean>;

/**
 * A tool call waiting for the user (app/wit.go card): what Wit wants, the call's recipients,
 * subject and body, where it sends data; Allow, Allow all (others of its run pending) and Reject.
 */
function ToolCard({
  a,
  many,
  busy,
  decide,
  session,
  t,
}: {
  a: Approval;
  many: boolean;
  busy: boolean;
  decide(accept: boolean, all: boolean): void;
  /** 本次对话都允许, where the host offers it and the call may take it. */
  session?(): void;
  t: T;
}) {
  let args: Record<string, unknown> = {};
  try {
    const v = JSON.parse(a.args || "{}");
    if (v && typeof v === "object") args = v;
  } catch {
    // not JSON: nothing to show of it
  }
  const fields: [string, string][] = [
    [t("wit.approval.to"), strList(args.to).join(", ")],
    [t("wit.approval.cc"), strList(args.cc).join(", ")],
    [t("wit.approval.subject"), typeof args.subject === "string" ? args.subject : ""],
  ];
  let body = "";
  for (const k of ["html", "body", "text"]) {
    const v = args[k];
    if (typeof v !== "string" || !v.trim()) continue;
    body =
      k === "html" ? (new DOMParser().parseFromString(v, "text/html").body.textContent ?? "") : v;
    body = clip(body.trim(), BODY);
    break;
  }
  const label = toolLabel(a.tool, t);
  return (
    <div className="overflow-hidden rounded-xl border-[0.5px] border-primary">
      <div className="flex flex-wrap items-center gap-2 bg-muted px-3.5 py-3 leading-5">
        <span className="font-semibold">{t("wit.approval.wants", label)}</span>
        <span className="font-mono text-sm">{a.tool}</span>
      </div>
      <div className="flex flex-col gap-1.5 px-3.5 py-3 text-md">
        {a.summary && a.summary !== label && <p className="text-muted-foreground">{a.summary}</p>}
        {fields.map(
          ([label, value]) =>
            value && (
              <div key={label} className="flex items-start gap-3.5">
                <span className="shrink-0 text-muted-foreground">{label}</span>
                <span className="min-w-0 break-words">{value}</span>
              </div>
            ),
        )}
        {body && <p className="text-muted-foreground">{body}</p>}
        {a.destinations.length > 0 && (
          <p className="text-destructive">
            {t("wit.approval.destinations", a.destinations.join(t("mail.list_sep")))}
          </p>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-2 border-muted border-t-[0.5px] px-3.5 py-2.5">
        <Button className="px-3.5 text-md" disabled={busy} onClick={() => decide(true, false)}>
          {t("wit.approval.allow")}
        </Button>
        {session && (
          <Button
            variant="outline"
            className="px-3 font-normal text-md"
            disabled={busy}
            onClick={session}
          >
            {t("wit.approval.allow_session")}
          </Button>
        )}
        {many && (
          <Button
            variant="outline"
            className="px-3 font-normal text-md"
            disabled={busy}
            onClick={() => decide(true, true)}
          >
            {t("wit.approval.allow_all")}
          </Button>
        )}
        <Button
          variant="outline"
          className="px-3 font-normal text-md"
          disabled={busy}
          onClick={() => decide(false, false)}
        >
          {t("wit.approval.reject")}
        </Button>
        {a.tool === "create_draft" && (
          <span className="ml-auto text-muted-foreground text-sm">
            {t("wit.approval.send_note")}
          </span>
        )}
      </div>
    </div>
  );
}

function Answer({
  m,
  live,
  nums,
  approvals,
  review,
  tools,
  onLink,
  onCite,
  onAgain,
  working,
  page,
  t,
}: {
  m: WitUIMessage;
  live: boolean;
  nums: Nums;
  approvals: Map<string, Approval>;
  review?: PanelReview;
  tools: (a: Approval) => ReactNode;
  onLink(url: string): void;
  onCite?(id: number): void;
  /** Asks the answer's question again in its place (重新生成), where it can be. */
  onAgain?(): void;
  /** What shows while the answer is live: the panel's working row, or the page's spinner. */
  working: ReactNode;
  page: boolean;
  t: T;
}) {
  const calls = m.parts.filter((p): p is DynamicToolUIPart => p.type === "dynamic-tool");
  // The tools line lists finished calls; a pending one shows as its card or the working row.
  const done = calls.filter(
    (c) =>
      c.state === "output-available" || c.state === "output-error" || c.state === "output-denied",
  );
  // Calls under way: no result yet and none waiting on the user.
  const going = live ? calls.filter((c) => c.state === "input-available") : [];
  const thought = m.parts.filter((p): p is ReasoningUIPart => p.type === "reasoning" && !!p.text);
  const thinking = live && thought.some((p) => p.state === "streaming");
  const cites = citesOf(m);
  const text = m.parts
    .flatMap((p) => (p.type === "text" && p.text.trim() ? [p.text] : []))
    .join("\n\n");
  const requested = calls.flatMap((c) => {
    const a = c.state === "approval-requested" ? approvals.get(c.approval.id) : undefined;
    return a ? [a] : [];
  });
  const waiting = requested.filter((a) => a.id === review?.approval);
  // [n] in the answer opens citation n (its message), when the host opens cited mail.
  const cite = (n: number) => {
    const id = [...nums].find(([, x]) => x.n === n)?.[0];
    return id !== undefined && onCite ? () => onCite(id) : undefined;
  };
  return (
    <div data-role="assistant" className="flex flex-col gap-2.5">
      {(thought.length > 0 || done.length > 0 || going.length > 0) && (
        // the answer's steps: what it thought, the tools it called, the calls under way
        <Trace className={page ? "[&_[data-slot=trace-line]]:text-base" : undefined}>
          {thought.length > 0 && (
            <TraceCollapsible>
              <TraceCollapsibleTrigger>
                <TraceLine status={thinking ? "running" : undefined}>
                  <TraceIcon>
                    <RiBrainLine />
                  </TraceIcon>
                  <TraceTitle>{t(thinking ? "wit.thinking" : "wit.reasoning")}</TraceTitle>
                </TraceLine>
              </TraceCollapsibleTrigger>
              <TraceCollapsibleContent className="whitespace-pre-wrap text-sm">
                {thought.map((p) => p.text).join("\n\n")}
              </TraceCollapsibleContent>
            </TraceCollapsible>
          )}
          {done.length > 0 && (
            <TraceCollapsible>
              <TraceCollapsibleTrigger>
                <TraceLine>
                  <TraceIcon>
                    <RiToolsLine />
                  </TraceIcon>
                  <TraceTitle>{toolsLabel(done, page, t)}</TraceTitle>
                </TraceLine>
              </TraceCollapsibleTrigger>
              <TraceCollapsibleContent className="flex flex-col gap-1">
                {done.map((c) => (
                  <code
                    key={c.toolCallId}
                    className={cn(
                      "break-all text-xs",
                      c.state === "output-error" && "text-destructive",
                    )}
                  >
                    {c.toolName} {clip(JSON.stringify(c.input ?? {}))}
                  </code>
                ))}
              </TraceCollapsibleContent>
            </TraceCollapsible>
          )}
          {going.map((c) => (
            <TraceLine key={c.toolCallId} status="running">
              <TraceIcon>
                <RiToolsLine />
              </TraceIcon>
              <TraceTitle>{toolLabel(c.toolName, t)}</TraceTitle>
            </TraceLine>
          ))}
        </Trace>
      )}
      {cites
        .flatMap((c) => {
          const k = nums.get(c.messageId);
          return k?.suspect ? [{ id: c.messageId, ...k }] : [];
        })
        .map((c) => (
          <Alert
            key={c.id}
            role="status"
            className="gap-x-2.5 gap-y-0 rounded-lg border-0 bg-(--warning-bg) px-3 py-2.5 text-(--warning-text) text-base"
          >
            <RiAlertLine />
            <AlertDescription className="text-base text-current leading-relaxed">
              {/* the quote is mail text: a plain string, never markup */}
              {c.quote ? t("wit.suspect_quote", `[${c.n}]`, c.quote) : t("wit.suspect", `[${c.n}]`)}
            </AlertDescription>
          </Alert>
        ))}
      {live && (!page || !text) && working}
      {text && (
        <Response
          text={text}
          streaming={live}
          size={page ? "page" : "panel"}
          t={t}
          onLink={onLink}
          cite={cite}
        />
      )}
      {text && !live && (
        <div className="-mt-1 -ml-1.5 flex">
          <Button
            variant="ghost"
            size="icon-xs"
            className="text-muted-foreground"
            aria-label={t("wit.copy")}
            title={t("wit.copy")}
            onClick={() => {
              void navigator.clipboard?.writeText(text);
              toast(t("wit.copied"));
            }}
          >
            <RiFileCopyLine />
          </Button>
          {onAgain && (
            <Button
              variant="ghost"
              size="icon-xs"
              className="text-muted-foreground"
              aria-label={t("wit.regenerate")}
              title={t("wit.regenerate")}
              onClick={onAgain}
            >
              <RiRefreshLine />
            </Button>
          )}
        </div>
      )}
      {cites.length > 0 && onCite && (
        // narrow: the cards under the answer; wide, the page's column shows them (Panel)
        <Citations items={cites} nums={nums} onOpen={onCite} t={t} className="@4xl:hidden" />
      )}
      {cites.length > 0 && !onCite && (
        <div className="flex flex-wrap gap-1">
          {cites.map((c) => (
            <HoverCard key={c.messageId}>
              <HoverCardTrigger render={<Badge variant="outline" className="cursor-default" />}>
                [{nums.get(c.messageId)?.n}]
              </HoverCardTrigger>
              <HoverCardContent className="flex flex-col gap-1">
                <span className="font-medium">{c.subject}</span>
                <span className="text-muted-foreground text-xs">
                  {c.from}
                  {c.date ? ` · ${new Date(c.date).toLocaleString()}` : ""}
                </span>
              </HoverCardContent>
            </HoverCard>
          ))}
        </div>
      )}
      {waiting.map((a) => (
        <div key={a.id} className="overflow-hidden rounded-xl border-[0.5px] border-primary">
          <div className="flex items-center gap-2 bg-muted px-3 py-2 leading-5">
            <span className="font-semibold">{t("wit.compose.review")}</span>
            <span className="font-mono text-foreground/80 text-sm">{a.tool}</span>
          </div>
          <div className="px-3 py-2 text-md">{review?.summary}</div>
          <div className="flex gap-1.5 border-muted border-t-[0.5px] px-3 py-2">
            {review?.actions}
          </div>
        </div>
      ))}
      {requested.filter((a) => !isDraftEdit(a.tool)).map(tools)}
    </div>
  );
}

/** The edit the page reviews: its card in the panel holds its summary, then its decisions. */
export type PanelReview = { approval: number; summary: ReactNode; actions: ReactNode };

/**
 * Wit's conversation and input, in either host. As the compose window's panel (layout "panel"):
 * what Wit knows (chips), 调整 / 翻译 (`info`), the conversation, and the input. As the Wit page
 * (layout "page"): the conversation centred, the input floating under it, the cited mail opening
 * by onCite in a column when the page is wide, else under each answer. Both inputs show the model
 * (`model`, its chip opening onModels); the panel's live answer says Wit writes the body. Links
 * open through the host's onLink (after the confirmation); its failure toasts.
 * A run's end reconciles: the history read again replaces what streamed, once it has the run.
 * `transport` must be the one whose follow() read `history`; a live run in it resumes into
 * its own answer, if the history's last. The edit the page reviews (`review`) shows as a card on
 * its answer; a tool's call waiting shows its own, decided by onDecide. A new `resume` streams that
 * run (resumed by a decision) onto the last answer if that is the run's, else into an answer of
 * its own. onRunning tells whether a run streams.
 */
export function Panel({
  info,
  history,
  locked,
  review,
  resume,
  onRunning,
  onDecide,
  onLink,
  onCite,
  transport,
  t,
  layout = "panel",
  model,
  onModels,
  models,
  ask: handed,
  onAsked,
  onSearch,
  onSession,
}: {
  info?: WitInfo;
  history: WitHistory;
  locked: boolean;
  review?: PanelReview;
  resume?: { run: number };
  onRunning?(on: boolean): void;
  onDecide?: Decide;
  onLink(url: string): Promise<void>;
  onCite?(id: number): void;
  transport: WitTransport;
  t: T;
  layout?: "panel" | "page";
  /** The default model's label, as the input's chip (onModels opens the model settings). */
  model?: string;
  onModels?(): void;
  /**
   * The conversation's model (ref, "" for the default) and the models to pick from: the chip
   * opens a menu of them (the settings last) instead of the settings.
   */
  models?: { ref: string; list(): Promise<ModelChoice[]>; pick(ref: string): void };
  /** A question the host hands over: asked once (by identity), then onAsked. */
  ask?: { text: string };
  onAsked?(): void;
  /** Searches mail for the quote picker (+ → 引用邮件, or @); without it, no quoting. */
  onSearch?(q: string): Promise<MailHit[]>;
  /** Decides a with 本次对话都允许 (the Wit page only); true once decided. */
  onSession?(a: Approval): Promise<boolean>;
}) {
  // The answer a live run goes on with (its own, the last) is taken out: the resumed stream
  // rebuilds it, then appends.
  const [start] = useState(() => {
    const messages = historyToMessages(history);
    const open =
      history.running && transport.owns(messages.at(-1), history.running)
        ? messages.pop()
        : undefined;
    return { messages, open };
  });
  const [deciding, setDeciding] = useState<number[]>([]);
  const [decided, setDecided] = useState<number[]>([]);
  const chat = useChat<WitUIMessage>({ transport, messages: start.messages });
  const [draft, setDraft] = useState("");
  const [quoted, setQuoted] = useState<MailHit[]>([]);
  const [choices, setChoices] = useState<ModelChoice[]>();
  // The question being edited: sending asks it in place of the one at index at.
  const [editing, setEditing] = useState<{ id: string; at: number }>();
  // What the conversation took, as last read (a run's end reads it again).
  const [usage, setUsage] = useState(history.usage);
  // The picker: from the + menu (at -1), or from an @ at draft[at] with what follows it as query.
  const [picking, setPicking] = useState<{ at: number; query: string }>();
  const textarea = useRef<HTMLTextAreaElement>(null);
  const pickerKeys = useRef<PickerKeys>(null);
  const full = quoted.length >= MAX_QUOTED;
  const running = chat.status === "submitted" || chat.status === "streaming";
  const { resumeStream } = chat;

  useEffect(() => {
    if (!history.running) return;
    transport.resume(start.open, history.running);
    void resumeStream();
  }, [history.running, transport, start, resumeStream]);

  useEffect(() => onRunning?.(running), [running, onRunning]);

  // A run over (its stream ended): the saved history replaces what streamed (core may drop text
  // for a slow subscriber), unless another stream started meanwhile or the read lacks the run.
  const wasRunning = useRef(false);
  const { setMessages } = chat;
  useEffect(() => {
    const ended = wasRunning.current && !running;
    wasRunning.current = running;
    const run = transport.run;
    if (!ended || !run) return;
    let live = true;
    transport.history().then(
      (h) => {
        if (!live || !h) return;
        setUsage(h.usage); // a run that saved nothing may still have taken tokens
        if (
          !h.messages.some((m) => m.runs.includes(run)) &&
          !h.approvals.some((a) => a.run === run)
        )
          return;
        setMessages(historyToMessages(h));
      },
      () => {}, // the stream stays as it is
    );
    return () => {
      live = false;
    };
  }, [running, transport, setMessages]);

  // A decision resumed a run: its events (logged since) go on in the last answer, if the run's.
  const last = chat.messages.at(-1);
  // biome-ignore lint/correctness/useExhaustiveDependencies: only a new resume streams
  useEffect(() => {
    if (!resume || running) return;
    transport.resume(transport.owns(last, resume.run) ? last : undefined, resume.run);
    void resumeStream();
  }, [resume]);

  const ask = (text: string, metadata?: WitMeta) => {
    if (locked || running || !text) return;
    void chat.sendMessage({ text, metadata });
  };
  const asked = useRef<object | undefined>(undefined);
  // biome-ignore lint/correctness/useExhaustiveDependencies: only a new handed question asks
  useEffect(() => {
    if (!handed || asked.current === handed) return;
    asked.current = handed;
    onAsked?.();
    ask(handed.text);
  }, [handed]);
  // A start refused (nothing answered the question): its text goes back to the input, not lost.
  const refused = chat.error && !running ? chat.messages.at(-1) : undefined;
  // biome-ignore lint/correctness/useExhaustiveDependencies: only a new refusal restores
  useEffect(() => {
    if (refused?.role !== "user" || draft) return;
    setDraft(refused.parts.flatMap((p) => (p.type === "text" ? [p.text] : [])).join(""));
    // its quotes too, and an edit stays one: sent again, it still replaces the old question
    setQuoted(refused.metadata?.hits ?? []);
    const r = refused.metadata?.rewind;
    // once its run started the old question is gone: sent again, it is a new one
    if (r !== undefined && !transport.askedInPlace(refused)) setEditing({ id: refused.id, at: r });
  }, [refused]);
  // A question's index in the saved history ("q<i>"); none for one not read back yet, or while a
  // call waits on the user (core rewinds no active run).
  const at = (m: WitUIMessage) => {
    const k = /^q(\d+)$/.exec(m.id);
    const waits = chat.messages.some((x) =>
      x.parts.some((p) => p.type === "data-approvals" && p.data.some((a) => a.state === "pending")),
    );
    return transport.rewinds && k && !waits ? Number(k[1]) : undefined;
  };
  // Asks again in place of question q: what came from it on goes, here and in the host.
  const again = (q: WitUIMessage, i: number, text: string, hits: MailHit[]) => {
    if (locked || running) return;
    chat.setMessages(chat.messages.slice(0, chat.messages.indexOf(q)));
    ask(text, { quoted: hits.map((h) => h.id), hits, rewind: i });
  };
  const submit = () => {
    const text = draft.trim();
    if (!text || locked || running) return;
    setDraft("");
    setQuoted([]);
    setPicking(undefined);
    const q = editing && chat.messages.find((m) => m.id === editing.id);
    setEditing(undefined);
    if (editing && q) return again(q, editing.at, text, quoted);
    ask(text, quoted.length > 0 ? { quoted: quoted.map((h) => h.id), hits: quoted } : undefined);
  };
  const textOf = (m: WitUIMessage) =>
    m.parts.flatMap((p) => (p.type === "text" ? [p.text] : [])).join("");
  const edit = (q: WitUIMessage) => {
    const i = at(q);
    if (i === undefined) return;
    setEditing({ id: q.id, at: i });
    setDraft(textOf(q));
    setQuoted(q.metadata?.hits ?? []);
    setTimeout(() => textarea.current?.focus());
  };
  const cancelEdit = () => {
    setEditing(undefined);
    setDraft("");
    setQuoted([]);
  };
  // Closing the picker gives the textarea its focus back, the caret at caret.
  const closePicker = (caret?: number) => {
    setPicking(undefined);
    setTimeout(() => {
      const el = textarea.current;
      el?.focus();
      if (el && caret !== undefined) el.setSelectionRange(caret, caret);
    });
  };
  const pick = (h: MailHit) => {
    setQuoted((q) => (q.some((x) => x.id === h.id) || q.length >= MAX_QUOTED ? q : [...q, h]));
    const at = picking?.at ?? -1;
    if (at < 0) return closePicker();
    // the @query becomes the chip
    setDraft((d) => d.slice(0, at) + d.slice(at + 1 + (picking?.query.length ?? 0)));
    closePicker(at);
  };
  // An @ typed at a word's start opens the picker; the text after it is its query, until a space,
  // or the @ goes. An @ inside a word (an address) is just text.
  const typed = (value: string, caret: number) => {
    setDraft(value);
    if (picking && picking.at >= 0) {
      const query = value.slice(picking.at + 1, caret);
      if (value[picking.at] !== "@" || caret <= picking.at || /\s/.test(query))
        setPicking(undefined);
      else setPicking({ at: picking.at, query });
      return;
    }
    const at = caret - 1;
    if (
      onSearch &&
      !full &&
      value.length === draft.length + 1 &&
      value[at] === "@" &&
      (at === 0 || /\s/.test(value[at - 1]))
    )
      setPicking({ at, query: "" });
  };
  const fail = (e: unknown) =>
    toast.error(witError(String((e as Error | undefined)?.message ?? e), t));
  const stop = () => void transport.stop().catch(fail);
  const link = (url: string) => void onLink(url).catch(fail);
  // The chip names the conversation's model: its choice's label once listed, else the ref's model.
  const chosen = models?.ref
    ? (choices?.find((c) => c.ref === models.ref)?.label ??
      models.ref.slice(models.ref.indexOf("/") + 1))
    : model;

  // Citations with core's numbers (the refs the model cites by); a message is suspect once any
  // citation of it is.
  const nums: Nums = new Map();
  const approvals = new Map<string, Approval>();
  for (const m of chat.messages) {
    for (const c of citesOf(m)) {
      const k = nums.get(c.messageId);
      if (k) {
        k.suspect ||= c.suspect;
        k.quote ||= c.quote;
      } else nums.set(c.messageId, { n: c.n, suspect: c.suspect, quote: c.quote });
    }
    for (const p of m.parts)
      if (p.type === "data-approvals") for (const a of p.data) approvals.set(String(a.id), a);
  }
  // A tool's call waiting: decided here (once decided, its card goes until the run resumes).
  const open = [...approvals.values()].filter(
    (a) => a.state === "pending" && !decided.includes(a.id),
  );
  const tools = (a: Approval) =>
    decided.includes(a.id) ? null : (
      <ToolCard
        key={a.id}
        a={a}
        many={open.filter((x) => x.run === a.run).length > 1}
        busy={!onDecide || deciding.length > 0}
        decide={async (accept, all) => {
          if (!onDecide || deciding.length > 0) return;
          const ids = all ? open.filter((x) => x.run === a.run).map((x) => x.id) : [a.id];
          setDeciding(ids);
          try {
            if (await onDecide(a, accept, all)) setDecided((d) => [...d, ...ids]);
          } finally {
            setDeciding([]);
          }
        }}
        session={
          onSession && a.sessionAllow
            ? async () => {
                if (deciding.length > 0) return;
                // the run's other pending calls of the tool are approved with it
                const ids = open
                  .filter((x) => x.run === a.run && x.tool === a.tool)
                  .map((x) => x.id);
                setDeciding(ids);
                try {
                  if (await onSession(a)) setDecided((d) => [...d, ...ids]);
                } finally {
                  setDeciding([]);
                }
              }
            : undefined
        }
        t={t}
      />
    );
  const page = layout === "page";
  // The page's column (max-w-202): 760 of text inside its 24 padding, as the design's.
  // A run going: the page's spinner; the panel's row, with Stop, says Wit writes the body while a
  // body edit's call is in (no result yet), else that it thinks.
  const writes =
    last?.role === "assistant" &&
    last.parts.some(
      (p) =>
        p.type === "dynamic-tool" &&
        isDraftEdit(p.toolName) &&
        (p.state === "input-streaming" || p.state === "input-available"),
    );
  const working = page ? (
    <Busy t={t} />
  ) : (
    <div className="flex items-center gap-2 text-muted-foreground">
      <span className="size-2 shrink-0 rounded-full bg-muted-foreground/65" />
      <span role="status">{t(writes ? "wit.compose.working" : "wit.thinking")}</span>
      <Button
        variant="outline"
        size="sm"
        className="ml-auto font-normal text-base text-foreground"
        onClick={stop}
      >
        {t("wit.stop")}
      </Button>
    </div>
  );
  const conversation = (
    <MessageScrollerProvider autoScroll>
      <MessageScroller className="min-h-0 flex-1">
        <MessageScrollerViewport>
          <MessageScrollerContent
            className={page ? "mx-auto w-full max-w-202 gap-5 p-6" : "gap-2.5 p-4"}
          >
            {chat.messages.map((m, i) => (
              <MessageScrollerItem
                key={m.id}
                messageId={m.id}
                // the panel: a turn's question and answer 10 apart, turns 18
                className={cn("group/turn", !page && m.role === "user" && i > 0 && "mt-2")}
              >
                {m.role === "user" ? (
                  <Message align="end">
                    <MessageContent className="items-end">
                      {/* the design's max-width is the text's (80%, the panel's 85%), padding aside */}
                      <Bubble
                        variant="secondary"
                        align="end"
                        className={
                          page ? "max-w-[calc(80%+--spacing(6))]" : "max-w-[calc(85%+--spacing(6))]"
                        }
                      >
                        <BubbleContent className="whitespace-pre-wrap border-0 text-md">
                          {textOf(m)}
                        </BubbleContent>
                      </Bubble>
                      {at(m) !== undefined && !running && !locked && (
                        // shown on the turn's hover, or when focused
                        <Button
                          variant="ghost"
                          size="icon-xs"
                          className="text-muted-foreground opacity-0 focus-visible:opacity-100 group-hover/turn:opacity-100"
                          aria-label={t("wit.edit")}
                          title={t("wit.edit")}
                          onClick={() => edit(m)}
                        >
                          <RiEditLine />
                        </Button>
                      )}
                    </MessageContent>
                  </Message>
                ) : (
                  <Answer
                    m={m}
                    live={running && m === last}
                    nums={nums}
                    approvals={approvals}
                    review={review}
                    tools={tools}
                    onLink={link}
                    onCite={onCite}
                    onAgain={(() => {
                      const q = chat.messages[i - 1];
                      const k = q?.role === "user" ? at(q) : undefined;
                      return m === last && !locked && k !== undefined
                        ? () => again(q, k, textOf(q), q.metadata?.hits ?? [])
                        : undefined;
                    })()}
                    working={working}
                    page={page}
                    t={t}
                  />
                )}
              </MessageScrollerItem>
            ))}
            {page && chat.messages.length === 0 && !running && !locked && (
              <div className="flex flex-col items-start gap-3 pt-[12vh]">
                <p className="text-muted-foreground">{t("wit.suggest")}</p>
                <div className="flex flex-wrap gap-2">
                  {[1, 2, 3, 4].map((n) => {
                    const q = t(`wit.suggest.${n}`);
                    return (
                      <Button
                        key={n}
                        variant="outline"
                        className="h-auto rounded-full px-3.5 py-1.5 font-normal text-md"
                        onClick={() => ask(q)}
                      >
                        {q}
                      </Button>
                    );
                  })}
                </div>
              </div>
            )}
            {running && last?.role !== "assistant" && working}
          </MessageScrollerContent>
        </MessageScrollerViewport>
        <MessageScrollerButton className="z-10">
          <RiArrowDownLine />
          <span className="sr-only">{t("wit.scroll_end")}</span>
        </MessageScrollerButton>
      </MessageScroller>
    </MessageScrollerProvider>
  );
  const error = chat.error && !running && (
    <Alert variant="destructive" className="border-0 bg-destructive/10">
      <AlertDescription>{witError(chat.error.message, t)}</AlertDescription>
      <AlertAction>
        <Button variant="ghost" size="sm" onClick={() => void chat.regenerate()}>
          {t("common.retry")}
        </Button>
      </AlertAction>
    </Alert>
  );
  // The context meter (the page's, once there is usage) leads the input's right-hand controls.
  const meter = page && (usage.input > 0 || usage.history > 0);
  // Send keeps the Primary look while nothing can be sent (an empty or locked input): only a
  // disabled control would grey the whole group.
  const idle = locked || !draft.trim() || running;
  const input = (
    <InputGroup className="relative gap-2 rounded-2xl border-0 bg-background px-3 py-2.5 shadow-(--shadow-float)">
      {editing && (
        <div className="flex items-center gap-2 text-muted-foreground text-sm">
          <RiEditLine className="size-3.5 shrink-0" />
          <span className="min-w-0 flex-1">{t("wit.editing")}</span>
          <InputGroupButton
            variant="ghost"
            size="icon-xs"
            aria-label={t("wit.edit.cancel")}
            onClick={cancelEdit}
          >
            <RiCloseLine />
          </InputGroupButton>
        </div>
      )}
      {picking && onSearch && (
        <QuotePicker
          search={onSearch}
          query={picking.at >= 0 ? picking.query : undefined}
          onPick={pick}
          onClose={() => closePicker()}
          keys={pickerKeys}
          t={t}
        />
      )}
      {quoted.length > 0 && (
        <QuoteChips
          items={quoted}
          onRemove={(id) => setQuoted((q) => q.filter((x) => x.id !== id))}
          t={t}
        />
      )}
      <InputGroupTextarea
        aria-label={t(page ? "wit.input" : "wit.compose.input")}
        placeholder={t(page ? "wit.placeholder" : "wit.compose.placeholder")}
        ref={textarea}
        value={draft}
        onChange={(e) => typed(e.target.value, e.target.selectionStart)}
        onKeyDown={(e) => {
          if (picking && picking.at >= 0 && pickerKeys.current?.key(e)) return;
          if (e.key !== "Enter" || e.shiftKey || e.nativeEvent.isComposing || e.keyCode === 229)
            return;
          e.preventDefault();
          submit();
        }}
        rows={2}
        // two lines at the least, as the design's rows="2"
        className={cn("min-h-9.5 p-0 text-md leading-5 md:text-md", page ? "max-h-44" : "max-h-40")}
      />
      <InputGroupAddon align="block-end" className="gap-2 p-0">
        {onSearch && (
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <InputGroupButton variant="ghost" size="icon-sm" aria-label={t("wit.add")}>
                  <RiAddLine className="size-4.5" />
                </InputGroupButton>
              }
            />
            <DropdownMenuContent side="top" className="min-w-45">
              <DropdownMenuItem disabled={full} onClick={() => setPicking({ at: -1, query: "" })}>
                <RiMailLine className="text-muted-foreground" />
                <span className="flex-1">{t("wit.quote")}</span>
                <span className="text-muted-foreground/70 text-sm">@</span>
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
        {meter && (
          <HoverCard>
            <HoverCardTrigger
              render={
                <InputGroupButton
                  variant="ghost"
                  size="sm"
                  className="ml-auto h-8 gap-1 px-2 font-normal text-muted-foreground text-sm tabular-nums"
                  aria-label={t("wit.usage")}
                />
              }
            >
              <RiPieChart2Line className="size-3.5" />
              {Math.min(100, Math.round(usage.history * 100))}%
            </HoverCardTrigger>
            <HoverCardContent side="top" className="flex w-72 flex-col gap-2 text-sm">
              <span className="font-medium">
                {t("wit.usage.history", Math.min(100, Math.round(usage.history * 100)))}
              </span>
              <Progress value={Math.min(100, usage.history * 100)} />
              <span className="text-muted-foreground">{t("wit.usage.note")}</span>
              {usage.context > 0 && (
                <span className="tabular-nums">
                  {t("wit.usage.last", usage.context.toLocaleString())}
                </span>
              )}
              {usage.input > 0 && (
                <span className="tabular-nums">
                  {t(
                    "wit.usage.total",
                    usage.input.toLocaleString(),
                    usage.output.toLocaleString(),
                  )}
                </span>
              )}
            </HoverCardContent>
          </HoverCard>
        )}
        {model && models ? (
          <DropdownMenu
            onOpenChange={(open) => {
              if (open) models.list().then(setChoices, fail);
            }}
          >
            <DropdownMenuTrigger
              render={
                <InputGroupButton
                  variant="ghost"
                  size="sm"
                  className={cn(
                    !meter && "ml-auto",
                    "h-8 min-w-0 gap-1 px-2 font-normal text-muted-foreground text-sm",
                  )}
                  title={t("wit.model.pick")}
                >
                  <span className="truncate">{chosen}</span>
                  <RiArrowDownSLine className="size-3" />
                </InputGroupButton>
              }
            />
            <DropdownMenuContent side="top" align="end" className="max-h-80 min-w-56">
              <DropdownMenuRadioGroup
                value={models.ref}
                onValueChange={(v: string) => models.pick(v)}
              >
                <DropdownMenuRadioItem value="">
                  {t("wit.model.default", model)}
                </DropdownMenuRadioItem>
                {choices?.map((c) => (
                  <DropdownMenuRadioItem key={c.ref} value={c.ref}>
                    {c.label}
                  </DropdownMenuRadioItem>
                ))}
              </DropdownMenuRadioGroup>
              {onModels && (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem onClick={onModels}>
                    <RiSettings3Line className="text-muted-foreground" />
                    {t("wit.model")}
                  </DropdownMenuItem>
                </>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        ) : (
          model && (
            <InputGroupButton
              variant="ghost"
              size="sm"
              className={cn(
                !meter && "ml-auto",
                "h-8 gap-1 px-2 font-normal text-muted-foreground text-sm",
              )}
              title={t("wit.model")}
              onClick={onModels}
            >
              {model}
              <RiArrowDownSLine className="size-3" />
            </InputGroupButton>
          )
        )}
        {running && page ? (
          <InputGroupButton
            className={cn(!model && !meter && "ml-auto")}
            variant="outline"
            size="icon-sm"
            aria-label={t("wit.stop")}
            onClick={stop}
          >
            <RiStopFill />
          </InputGroupButton>
        ) : (
          <InputGroupButton
            className={cn(!model && !meter && "ml-auto")}
            variant="default"
            size="icon-sm"
            aria-label={t("wit.send")}
            aria-disabled={idle}
            onClick={submit}
          >
            <RiArrowUpLine />
          </InputGroupButton>
        )}
      </InputGroupAddon>
    </InputGroup>
  );

  // the conversation fades out under the input area, which is solid to the card's bottom edge
  const fade = (
    <div
      aria-hidden
      className="pointer-events-none absolute inset-x-0 bottom-full h-8 bg-linear-to-b from-transparent to-background"
    />
  );
  // a card on the window's material: the native layout's content cards
  const card = "rounded-xl border-[0.5px] bg-background shadow-(--shadow-xs)";
  if (page) {
    const cites = citesOf(...chat.messages);
    return (
      <div className="@container flex h-full min-h-0 gap-2">
        <div className={cn("flex min-w-0 flex-1 flex-col overflow-hidden", card)}>
          {conversation}
          <div className="relative bg-background">
            {fade}
            <div className="mx-auto flex w-full max-w-202 flex-col gap-2 p-6">
              {error}
              {input}
            </div>
          </div>
        </div>
        {cites.length > 0 && onCite && (
          // wide: a card of 320 (narrow: under each answer, Answer)
          <aside className={cn("hidden w-80 shrink-0 flex-col overflow-auto p-4 @4xl:flex", card)}>
            <Citations items={cites} nums={nums} onOpen={onCite} t={t} />
          </aside>
        )}
      </div>
    );
  }
  // 调整 / 翻译: a tray the input overlaps from below; locked, they do nothing but keep their look.
  const trayButton = (Icon: typeof RiEqualizerLine, label: string) => (
    <Button
      variant="ghost"
      size="sm"
      className="gap-1.5 px-2 font-normal text-sm disabled:opacity-100"
    >
      <Icon className="size-3.5 text-muted-foreground" />
      {label}
      <RiArrowDownSLine className="size-3 text-muted-foreground" />
    </Button>
  );
  // No model, a plugin translates: the 翻译 menu alone, the tray standing on its own.
  const only = info?.translateOnly ?? false;
  // a card filling its resizable panel (320 by default) beside the editor's
  return (
    <aside className={cn("flex h-full w-full flex-col overflow-hidden", card)}>
      {info && !only && info.chips.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5 px-4 py-3">
          {info.chips.map((c) => (
            <Badge
              key={c}
              variant="secondary"
              className="h-auto max-w-full truncate rounded-sm font-normal text-sm"
            >
              {c}
            </Badge>
          ))}
        </div>
      )}
      {conversation}
      <div className="relative bg-background px-4 pb-3">
        {fade}
        {error && <div className="pb-2">{error}</div>}
        {info && (
          <div
            data-tray
            className={cn(
              "mx-1 flex gap-1 bg-muted px-1 pt-1",
              only ? "rounded-2xl pb-1" : "-mb-4 rounded-t-2xl pb-5",
            )}
          >
            {!only && (
              <DropdownMenu>
                <DropdownMenuTrigger
                  disabled={locked}
                  render={trayButton(RiEqualizerLine, t("wit.compose.adjust"))}
                />
                <DropdownMenuContent className="min-w-45">
                  {info.skills.map((s) => (
                    <DropdownMenuItem
                      key={s.id}
                      onClick={() => ask(s.label, { skill: s.id, lang: "" })}
                    >
                      {s.label}
                      <span className="ml-auto text-muted-foreground text-sm">{s.hint}</span>
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            )}
            <DropdownMenu>
              <DropdownMenuTrigger
                disabled={locked}
                render={trayButton(RiTranslate2, t("wit.compose.translate"))}
              />
              <DropdownMenuContent className="min-w-45">
                {info.languages.map((l) => (
                  <DropdownMenuItem
                    key={l.tag}
                    onClick={() =>
                      ask(t("wit.compose.translate_to", l.name), {
                        skill: "translate-draft",
                        lang: l.tag,
                      })
                    }
                  >
                    {l.name}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        )}
        {only ? (
          <p className="px-2 pt-2 text-muted-foreground text-sm">
            {t("wit.compose.translate_only")}
            {onModels && (
              <>
                {" "}
                <Button
                  variant="link"
                  size="sm"
                  className="h-auto p-0 align-baseline text-sm"
                  onClick={onModels}
                >
                  {t("wit.compose.translate_only.models")}
                </Button>
              </>
            )}
          </p>
        ) : (
          input
        )}
      </div>
    </aside>
  );
}
