import { useChat } from "@ai-sdk/react";
import {
  RiAddLine,
  RiAlertLine,
  RiArrowDownLine,
  RiArrowDownSLine,
  RiArrowUpLine,
  RiBrainLine,
  RiCheckboxCircleLine,
  RiCloseCircleLine,
  RiDraftLine,
  RiEditLine,
  RiEqualizerLine,
  RiErrorWarningLine,
  RiExternalLinkLine,
  RiFileCopyLine,
  RiGlobalLine,
  RiLoaderLine,
  RiMailLine,
  RiRefreshLine,
  RiSearchLine,
  RiSettings3Line,
  RiShieldLine,
  RiSparkling2Line,
  RiStopFill,
  RiToolsLine,
  RiTranslate2,
} from "@remixicon/react";
import type { DynamicToolUIPart, ReasoningUIPart } from "ai";
import { cn } from "cn";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { Alert, AlertAction, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Bubble, BubbleContent } from "@/components/ui/bubble";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
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
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
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
import { currentLocale, type T } from "@/shared/i18n";
import { Citations, type Nums } from "./Citations";
import { MailCard, senderName } from "./MailCard";
import { type PickerKeys, QuoteChips, QuotePicker } from "./Quote";
import { Response } from "./Response";
import { historyToMessages, type WitMeta, type WitTransport, type WitUIMessage } from "./transport";

/** The emails a question quotes at most (core maxQuoted). */
const MAX_QUOTED = 10;

// ponytail: shadcn's spinner.tsx fails the typecheck (children on a Remix icon); its markup here.
/** How full the history is (0–1), as a ring: the attention colour from 80%. */
const UsageRing = ({ value }: { value: number }) => {
  const r = 6;
  const c = 2 * Math.PI * r;
  return (
    <svg
      viewBox="0 0 16 16"
      className={cn("size-4", value >= 0.8 ? "text-amber-500" : "text-muted-foreground")}
      aria-hidden="true"
    >
      <circle cx="8" cy="8" r={r} fill="none" strokeWidth="2" className="stroke-border" />
      <circle
        cx="8"
        cy="8"
        r={r}
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeDasharray={`${c * Math.min(1, value)} ${c}`}
        transform="rotate(-90 8 8)"
      />
    </svg>
  );
};

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
      if (i < 0) out.push({ ...c, callIds: [...c.callIds] });
      else {
        out[i].suspect ||= c.suspect;
        out[i].quote ||= c.quote;
        // the calls that showed it: each step credits every one
        for (const id of c.callIds) if (!out[i].callIds.includes(id)) out[i].callIds.push(id);
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

/** A step's icon: what the tool does with mail, or a tool. */
const stepIcon = (tool: string) =>
  tool === "search_mail"
    ? RiSearchLine
    : /message|thread|attachment/.test(tool)
      ? RiMailLine
      : RiToolsLine;

/**
 * An answer's steps in order, as a timeline: what it thought, each call (a search with its query
 * and the senders it found), the one under way spinning.
 */
function Steps({
  m,
  live,
  cites,
  t,
}: {
  m: WitUIMessage;
  /** The calls under way. */
  live: DynamicToolUIPart[];
  cites: Citation[];
  t: T;
}) {
  const steps = m.parts.filter(
    (p): p is ReasoningUIPart | DynamicToolUIPart =>
      (p.type === "reasoning" && !!p.text) || p.type === "dynamic-tool",
  );
  return (
    <ol aria-label={t("wit.reasoning")} className="flex flex-col pt-1">
      {steps.map((p, i) => {
        const going = p.type === "dynamic-tool" && live.includes(p);
        const Icon =
          p.type === "reasoning" ? RiBrainLine : going ? RiLoaderLine : stepIcon(p.toolName);
        const query =
          p.type === "dynamic-tool" && typeof (p.input as { query?: unknown })?.query === "string"
            ? (p.input as { query: string }).query
            : "";
        const senders =
          p.type === "dynamic-tool"
            ? [
                ...new Set(
                  cites
                    .filter((c) => c.callIds.includes(p.toolCallId))
                    .map((c) => senderName(c.from)),
                ),
              ]
            : [];
        return (
          <li key={p.type === "reasoning" ? `r${i}` : p.toolCallId} className="flex gap-2.5">
            <span aria-hidden="true" className="flex flex-col items-center">
              <span className="flex size-5 items-center justify-center text-muted-foreground">
                <Icon className={cn("size-3.5", going && "animate-spin")} />
              </span>
              {i < steps.length - 1 && <span className="w-px flex-1 bg-border" />}
            </span>
            {p.type === "reasoning" ? (
              <span className="whitespace-pre-wrap pb-3 text-muted-foreground text-sm leading-relaxed">
                {p.text}
              </span>
            ) : (
              <span className="flex min-w-0 flex-col gap-1.5 pb-3 text-sm">
                <span className="flex flex-wrap items-center gap-1.5">
                  <code className={cn("text-xs", p.state === "output-error" && "text-destructive")}>
                    {p.toolName}
                  </code>
                  {query && (
                    <span className="rounded-sm bg-muted px-1.5 font-mono text-xs">
                      {clip(query)}
                    </span>
                  )}
                </span>
                {senders.length > 0 && (
                  <span className="flex flex-wrap gap-1">
                    {senders.slice(0, 5).map((n) => (
                      <span
                        key={n}
                        className="rounded-full border-[0.5px] px-2 text-muted-foreground text-xs leading-5"
                      >
                        {n}
                      </span>
                    ))}
                    {senders.length > 5 && (
                      <span className="text-muted-foreground text-xs leading-5">
                        +{senders.length - 5}
                      </span>
                    )}
                  </span>
                )}
              </span>
            )}
          </li>
        );
      })}
    </ol>
  );
}

/** Decides approval a, or with all every pending one of its run; true once decided. */
export type Decide = (a: Approval, accept: boolean, all: boolean) => Promise<boolean>;

/**
 * A tool call waiting for the user (app/wit.go card): what Wit wants, the tool, its plugin and that
 * it waits; the call's recipients, subject and body; the hosts it sends data to; Reject, 本次对话
 * 都允许 (where offered) and Allow. A run's several calls get one header over them (Approvals).
 */
function ToolCard({
  a,
  busy,
  decide,
  session,
  t,
}: {
  a: Approval;
  busy: boolean;
  decide(accept: boolean): void;
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
    <div className="overflow-hidden rounded-xl border-[0.5px] bg-background shadow-sm">
      <div className="flex flex-wrap items-center gap-2 border-b-[0.5px] bg-sidebar px-3.5 py-2.5 leading-5">
        <span className="font-semibold">{t("wit.approval.wants", label)}</span>
        <span className="font-mono text-muted-foreground text-sm">{a.tool}</span>
        <span className="ml-auto flex gap-1.5">
          {a.tool.startsWith("plugin_") && (
            <Badge variant="secondary">{t("wit.approval.plugin")}</Badge>
          )}
          <Badge
            variant="outline"
            className="border-(--warning-border) bg-(--warning-bg) text-(--warning-text)"
          >
            {t("wit.approval.pending")}
          </Badge>
        </span>
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
        {body && <p className="line-clamp-3 text-muted-foreground">{body}</p>}
        {a.destinations.length > 0 && (
          <div className="flex items-start gap-3.5">
            <span className="shrink-0 text-muted-foreground">{t("wit.approval.sends_to")}</span>
            <span className="flex flex-wrap gap-1.5">
              {a.destinations.map((d) => (
                <span
                  key={d}
                  className="flex h-5.5 items-center gap-1 rounded-full border-[0.5px] px-2 text-sm"
                >
                  <RiGlobalLine className="size-3 text-muted-foreground" />
                  {d}
                </span>
              ))}
            </span>
          </div>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-2 border-muted border-t-[0.5px] px-3.5 py-2.5">
        {a.tool === "create_draft" && (
          <span className="flex items-center gap-1.5 text-muted-foreground text-sm">
            <RiShieldLine className="size-3.5" />
            {t("wit.approval.send_note")}
          </span>
        )}
        <span className="ml-auto flex flex-wrap gap-2">
          <Button
            variant="outline"
            className="px-3 font-normal text-md"
            disabled={busy}
            onClick={() => decide(false)}
          >
            {t("wit.approval.reject")}
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
          <Button className="px-3.5 text-md" disabled={busy} onClick={() => decide(true)}>
            {t("wit.approval.allow")}
          </Button>
        </span>
      </div>
    </div>
  );
}

/** A decided approval as one line (已允许 / 已拒绝 / 没有完成 · what), a draft Wit made as its card. */
function Settled({ a, onDraft, t }: { a: Approval; onDraft?(id: number): Promise<void>; t: T }) {
  const label = toolLabel(a.tool, t);
  // the draft sent or deleted since this history was read: opening it fails, and its card goes
  const [gone, setGone] = useState(false);
  const [Icon, key, tone] =
    a.state === "done"
      ? [RiCheckboxCircleLine, "wit.approval.allowed", "text-green-600"]
      : a.state === "failed"
        ? [RiErrorWarningLine, "wit.approval.failed", "text-destructive"]
        : [RiCloseCircleLine, "wit.approval.rejected", "text-muted-foreground"];
  let subject = "";
  try {
    const v = JSON.parse(a.args || "{}");
    if (typeof v?.subject === "string") subject = v.subject;
  } catch {
    // not JSON: no subject to show
  }
  return (
    <>
      <span className="flex items-center gap-2 text-muted-foreground text-sm">
        <Icon className={cn("size-3.5 shrink-0", tone)} />
        <span>{t(key, label)}</span>
        {label !== a.tool && <span className="font-mono text-muted-foreground/70">{a.tool}</span>}
      </span>
      {a.tool === "create_draft" && a.state === "done" && a.draft > 0 && !gone && (
        <div className="flex items-center gap-3 rounded-xl border-[0.5px] bg-background px-3 py-2.5">
          <span className="flex size-8 shrink-0 items-center justify-center rounded-lg border-[0.5px] bg-sidebar text-muted-foreground">
            <RiDraftLine className="size-4" />
          </span>
          <span className="flex min-w-0 flex-1 flex-col">
            <span className="truncate font-medium">{subject || t("mail.no_subject")}</span>
            <span className="text-muted-foreground text-sm">{t("wit.draft.saved")}</span>
          </span>
          {onDraft && (
            <Button
              variant="ghost"
              size="sm"
              className="font-normal"
              onClick={() =>
                onDraft(a.id).catch(
                  (e) => (e as Error | undefined)?.message === "wit.draft.gone" && setGone(true),
                )
              }
            >
              {t("wit.draft.open")}
              <RiExternalLinkLine className="text-muted-foreground" />
            </Button>
          )}
        </div>
      )}
    </>
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
  onQuote,
  onDraft,
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
  /** The answer's calls waiting for the user, as cards (with a header over a run's several). */
  tools: (as: Approval[]) => ReactNode;
  onDraft?(approval: number): Promise<void>;
  onLink(url: string): void;
  onCite?(id: number): void;
  /** Adds a mail to the input's quoted mail (引用邮件). */
  onQuote(h: MailHit): void;
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
  // a message found gone since (opening it failed) is gone in every answer that cites it
  const cites = citesOf(m).map((c) => (nums.get(c.messageId)?.c.gone ? { ...c, gone: true } : c));
  const [sources, setSources] = useState(false);
  const text = m.parts
    .flatMap((p) => (p.type === "text" && p.text.trim() ? [p.text] : []))
    .join("\n\n");
  const requested = calls.flatMap((c) => {
    const a = c.state === "approval-requested" ? approvals.get(c.approval.id) : undefined;
    return a ? [a] : [];
  });
  const waiting = requested.filter((a) => a.id === review?.approval);
  // Its own calls the user decided (draft edits have their own rows), as they stand now: an
  // answer's approvals are its run's (a call ID alone may be another run's).
  const settled = m.parts
    .flatMap((p) => (p.type === "data-approvals" ? p.data : []))
    .map((a) => approvals.get(String(a.id)) ?? a)
    .filter(
      (a, i, all) =>
        all.findIndex((x) => x.id === a.id) === i &&
        ["done", "failed", "rejected", "stale"].includes(a.state) &&
        !isDraftEdit(a.tool),
    );
  // [n] in the answer shows citation n's mail in a popover, [[n]] as a card in the text: either
  // opens it (when the host opens cited mail) or quotes it in the input.
  const mail = (n: number, card: boolean) => {
    const c = [...nums.values()].find((x) => x.n === n)?.c;
    if (!c) return undefined;
    return (
      <MailCard
        card={card}
        from={c.from}
        date={c.date}
        subject={c.subject}
        snippet={c.snippet}
        // a message deleted since: nothing to open or quote
        onOpen={onCite && !c.gone ? () => onCite(c.messageId) : undefined}
        onQuote={
          c.gone
            ? undefined
            : () => onQuote({ id: c.messageId, subject: c.subject, from: c.from, date: c.date })
        }
        t={t}
      />
    );
  };
  return (
    <div data-role="assistant" className="flex flex-col gap-2.5">
      {(thought.length > 0 || done.length > 0 || going.length > 0) && (
        // the answer's steps: what it thought, the tools it called, the calls under way
        <Trace className={page ? "[&_[data-slot=trace-line]]:text-base" : undefined}>
          {(thought.length > 0 || done.length > 0) && (
            <TraceCollapsible>
              <TraceCollapsibleTrigger>
                <TraceLine status={thinking ? "running" : undefined}>
                  <TraceIcon>
                    <RiBrainLine />
                  </TraceIcon>
                  <TraceTitle>
                    {thinking
                      ? t("wit.thinking")
                      : [
                          thought.length > 0 && t("wit.reasoning"),
                          done.length > 0 && toolsLabel(done, page, t),
                        ]
                          .filter(Boolean)
                          .join(" · ")}
                  </TraceTitle>
                </TraceLine>
              </TraceCollapsibleTrigger>
              <TraceCollapsibleContent>
                <Steps m={m} live={going} cites={cites} t={t} />
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
          confirm={!page}
          cite={(n) => mail(n, false)}
          card={(n) => mail(n, true)}
        />
      )}
      {((text && !live) || (cites.length > 0 && onCite)) && (
        <div className="-mt-1 -ml-1.5 flex items-center">
          {text && !live && (
            <>
              <Button
                variant="ghost"
                size="icon-xs"
                className="text-muted-foreground"
                aria-label={t("wit.copy")}
                title={t("wit.copy")}
                onClick={() =>
                  (navigator.clipboard?.writeText(text) ?? Promise.reject()).then(
                    () => toast(t("wit.copied")),
                    () => toast.error(t("wit.copy_failed")),
                  )
                }
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
            </>
          )}
          {cites.length > 0 && onCite && (
            <>
              {text && !live && <span aria-hidden="true" className="mx-1.5 h-3.5 w-px bg-border" />}
              <Button
                variant="ghost"
                size="xs"
                className="font-normal text-muted-foreground"
                aria-expanded={sources}
                onClick={() => setSources((o) => !o)}
              >
                {t("wit.citations.count", cites.length)}
                <RiArrowDownSLine className={cn("transition-transform", sources && "rotate-180")} />
              </Button>
            </>
          )}
        </div>
      )}
      {cites.length > 0 && onCite && sources && (
        <Citations items={cites} nums={nums} onOpen={onCite} t={t} />
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
      {tools(requested.filter((a) => !isDraftEdit(a.tool)))}
      {settled.map((a) => (
        <Settled key={a.id} a={a} onDraft={onDraft} t={t} />
      ))}
    </div>
  );
}

/** The edit the page reviews: its card in the panel holds its summary, then its decisions. */
export type PanelReview = { approval: number; summary: ReactNode; actions: ReactNode };

/**
 * Wit's conversation and input, in either host. As the compose window's panel (layout "panel"):
 * what Wit knows (chips), 调整 / 翻译 (`info`), the conversation, and the input. As the Wit page
 * (layout "page"): the conversation centred, the input floating under it, each answer's cited
 * mail folded under its actions (引用了 N 封邮件), opening by onCite. Both inputs show the model
 * (`model`, its chip opening onModels); the panel's live answer says Wit writes the body. Links
 * open through the host's onLink (after the confirmation); its failure toasts.
 * A run's end reconciles: the history read again replaces what streamed, once it has the run.
 * `transport` must be the one whose follow() read `history`; a live run in it resumes into
 * its own answer, if the history's last. The edit the page reviews (`review`) shows as a card on
 * its answer; a tool's call waiting shows its own, decided by onDecide. A new `resume` streams that
 * run (resumed by a decision) onto the last answer if that is the run's, else into an answer of
 * its own. onRunning tells whether a run streams.
 */
/** The 翻译 menu: names in the app's language, which comes first, the rest alphabetical. */
function translateMenu(languages: { tag: string; name: string }[], locale: string) {
  const base = (tag: string) => tag.split(/[-_]/)[0].toLowerCase();
  let names: Intl.DisplayNames | undefined;
  try {
    names = new Intl.DisplayNames([locale], { type: "language" });
  } catch {}
  const label = (l: { tag: string; name: string }) => {
    try {
      const n = names?.of(l.tag);
      if (n) return n.charAt(0).toUpperCase() + n.slice(1);
    } catch {}
    return l.name;
  };
  const first = (tag: string) => (base(tag) === base(locale) ? 0 : 1);
  return languages
    .map((l) => ({ tag: l.tag, label: label(l) }))
    .sort((a, b) => first(a.tag) - first(b.tag) || a.label.localeCompare(b.label, locale));
}

/** The host's answer that a cited message was deleted since (WitPage.CheckCited). */
const isGone = (e: unknown) => (e as Error | undefined)?.message === "wit.mail.gone";

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
  onCheckCited,
  onDraft,
  transport,
  t,
  layout = "panel",
  model,
  onModels,
  models,
  effort,
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
  /** Opens cited message id; a rejection (the message gone since) drops its actions. */
  onCite?(id: number): Promise<void> | void;
  /** Checks cited message id still exists before it is quoted; rejects when it is gone. */
  onCheckCited?(id: number): Promise<void>;
  /** Opens the draft an allowed create_draft made (its approval), where the host can. */
  onDraft?(approval: number): Promise<void>;
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
  /** The conversation's reasoning effort ("" for the model's default), picked beside the model. */
  effort?: { value: string; pick(value: string): void };
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
  // as they are now, for what a run's end decides after reading the history
  const chatRef = useRef(chat);
  chatRef.current = chat;
  const draftRef = useRef(draft);
  draftRef.current = draft;
  const [quoted, setQuoted] = useState<MailHit[]>([]);
  // a submit checking its quoted mail
  const checking = useRef(false);
  // cited messages found deleted since this conversation was read
  const [gone, setGone] = useState<number[]>([]);
  const [choices, setChoices] = useState<ModelChoice[]>();
  // The question being edited: sending asks it in place of the one at index at.
  const [editing, setEditing] = useState<{ at: number }>();
  // what the input holds now, for a submit whose checks answer later
  const latest = useRef({ draft, quoted, editing });
  latest.current = { draft, quoted, editing };
  // What asking in place took off the conversation shown: back if the host refuses the question.
  const shelved = useRef<WitUIMessage[]>([]);
  // What the conversation took, as last read (a run's end reads it again).
  const [usage, setUsage] = useState(history.usage);
  const [compacting, setCompacting] = useState(false);
  // The picker: from the + menu (at -1), or from an @ at draft[at] with what follows it as query.
  const [picking, setPicking] = useState<{ at: number; query: string }>();
  const textarea = useRef<HTMLTextAreaElement>(null);
  const pickerKeys = useRef<PickerKeys>(null);
  const full = quoted.length >= MAX_QUOTED;
  const running = chat.status === "submitted" || chat.status === "streaming";
  const { resumeStream } = chat;
  const locale = currentLocale();
  const menuLanguages = useMemo(
    () => translateMenu(info?.languages ?? [], locale),
    [info?.languages, locale],
  );

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
        ) {
          // A question asked in place, stopped or failed: it replaced nothing. The conversation
          // shows as it is, the question back in the input, a failure as a toast (no 重试: what it
          // would retry is no longer shown).
          const c = chatRef.current;
          const q = c.messages.findLast((m) => m.role === "user");
          if (q?.metadata?.rewind === undefined) return;
          if (c.error) {
            toast.error(witError(c.error.message, t));
            c.clearError();
          }
          setMessages(historyToMessages(h));
          putBackRef.current(q);
          return;
        }
        setMessages(historyToMessages(h));
      },
      () => {}, // the stream stays as it is
    );
    return () => {
      live = false;
    };
  }, [running, transport, setMessages, t]);

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
  // A question not asked after all goes back to the input, unless something was typed since: its
  // text, its quotes, and an edit still one (sent again, it still replaces the old question).
  const putBack = (q: WitUIMessage) => {
    if (draftRef.current) return;
    setDraft(textOf(q));
    setQuoted(q.metadata?.hits ?? []);
    const r = q.metadata?.rewind;
    if (r !== undefined) setEditing({ at: r });
  };
  const putBackRef = useRef(putBack);
  putBackRef.current = putBack;
  // A start refused (nothing answered the question): its text goes back to the input, not lost.
  const refused = chat.error && !running ? chat.messages.at(-1) : undefined;
  // biome-ignore lint/correctness/useExhaustiveDependencies: only a new refusal restores
  useEffect(() => {
    if (refused?.role !== "user") return;
    putBack(refused);
    if (refused.metadata?.rewind === undefined) return;
    // the host changed nothing: what it would have replaced comes back, the refusal as a toast
    toast.error(witError(chat.error?.message ?? "", t));
    setMessages([...chat.messages.slice(0, -1), ...shelved.current]);
    chat.clearError();
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
  // Asks again in place of the question at index i: what came from it on goes, here and in the host.
  const again = (i: number, text: string, hits: MailHit[]) => {
    if (locked || running) return;
    const k = chat.messages.findIndex((m) => m.id === `q${i}`);
    shelved.current = k < 0 ? [] : chat.messages.slice(k);
    if (k >= 0) chat.setMessages(chat.messages.slice(0, k));
    ask(text, { quoted: hits.map((h) => h.id), hits, rewind: i });
  };
  const submit = () => {
    const text = draft.trim();
    if (!text || locked || running) return;
    if (text === "/compact" && transport.compacts && !editing) {
      setDraft("");
      if (!compacting) compact();
      return;
    }
    // The quoted mail is checked first, where the host can: one deleted since (a sync) would make
    // core refuse the whole question. A gone one leaves the input; the rest wait to be sent again.
    // It sends only what is still in the input once the checks answer, and nothing after a check
    // that failed for another reason (the host showed it; Enter tries again).
    if (onCheckCited && quoted.length > 0) {
      if (checking.current) return;
      checking.current = true;
      const asked = { text, ids: quoted.map((h) => h.id).join(), editing };
      void Promise.allSettled(quoted.map((h) => onCheckCited(h.id))).then((rs) => {
        checking.current = false;
        const failed = (i: number) => rs[i].status === "rejected";
        const gone = quoted
          .filter((_, i) => failed(i) && isGone((rs[i] as PromiseRejectedResult).reason))
          .map((h) => h.id);
        for (const id of gone) markGone(id);
        setQuoted((q) => q.filter((h) => !gone.includes(h.id)));
        const now = latest.current;
        const same =
          now.draft.trim() === asked.text &&
          now.quoted.map((h) => h.id).join() === asked.ids &&
          now.editing === asked.editing;
        if (same && !rs.some((_, i) => failed(i))) send(text, quoted);
      });
      return;
    }
    send(text, quoted);
  };
  const send = (text: string, hits: MailHit[]) => {
    setDraft("");
    setQuoted([]);
    setPicking(undefined);
    setEditing(undefined);
    if (editing) return again(editing.at, text, hits);
    ask(text, hits.length > 0 ? { quoted: hits.map((h) => h.id), hits } : undefined);
  };
  const textOf = (m: WitUIMessage) =>
    m.parts.flatMap((p) => (p.type === "text" ? [p.text] : [])).join("");
  const edit = (q: WitUIMessage) => {
    const i = at(q);
    if (i === undefined) return;
    setEditing({ at: i });
    setDraft(textOf(q));
    setQuoted(q.metadata?.hits ?? []);
    setTimeout(() => textarea.current?.focus());
  };
  // The edited question's index in the messages (-1: none).
  const editingAt = editing ? chat.messages.findIndex((m) => m.id === `q${editing.at}`) : -1;
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
  // A mail joins the quoted ones once, up to MAX_QUOTED.
  const quote = (h: MailHit) =>
    setQuoted((q) => (q.some((x) => x.id === h.id) || q.length >= MAX_QUOTED ? q : [...q, h]));
  const pick = (h: MailHit) => {
    quote(h);
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
  // Compacting on demand (the meter's button, or /compact sent): the usage is read again once the
  // summary is saved.
  const compact = () => {
    setCompacting(true);
    transport
      .compact()
      .then(() => {
        toast.success(t("wit.usage.compacted"));
        return transport.history();
      })
      .then((h) => h && setUsage(h.usage), fail)
      .finally(() => setCompacting(false));
  };
  const link = (url: string) => void onLink(url).catch(fail);
  // The chip names the conversation's model: its choice's name once listed, else the ref's model.
  const chosen = models?.ref
    ? (choices?.find((c) => c.ref === models.ref)?.name ??
      models.ref.slice(models.ref.indexOf("/") + 1))
    : model;
  // The settings' default among them (none: it is not a model listed).
  const listedDefault = choices?.find((c) => c.default);
  // The menu groups the choices by their service, in the order they come.
  const services = new Map<string, ModelChoice[]>();
  for (const c of choices ?? []) services.set(c.service, [...(services.get(c.service) ?? []), c]);

  // Citations with core's numbers (the refs the model cites by); a message is suspect once any
  // citation of it is.
  // Opens a cited message; one deleted since (the host says so: wit.mail.gone, not any failure,
  // which may pass) loses its actions here.
  const markGone = (id: number) => setGone((g) => (g.includes(id) ? g : [...g, id]));
  const openCited =
    onCite &&
    ((id: number) => void Promise.resolve(onCite(id)).catch((e) => isGone(e) && markGone(id)));
  // Quotes a cited message once the host says it still exists (else it is gone, unquoted).
  const quoteCited = (h: MailHit) =>
    onCheckCited
      ? void onCheckCited(h.id).then(
          () => quote(h),
          (e) => isGone(e) && markGone(h.id),
        )
      : quote(h);
  const nums: Nums = new Map();
  const approvals = new Map<string, Approval>();
  for (const m of chat.messages) {
    for (const c of citesOf(m)) {
      const k = nums.get(c.messageId);
      if (k) {
        k.suspect ||= c.suspect;
        k.quote ||= c.quote;
      } else
        nums.set(c.messageId, {
          n: c.n,
          suspect: c.suspect,
          quote: c.quote,
          c: gone.includes(c.messageId) ? { ...c, gone: true } : c,
        });
    }
    for (const p of m.parts)
      if (p.type === "data-approvals") for (const a of p.data) approvals.set(String(a.id), a);
  }
  // A tool's call waiting: decided here (once decided, its card goes until the run resumes).
  const open = [...approvals.values()].filter(
    (a) => a.state === "pending" && !decided.includes(a.id),
  );
  // Decides a, or with all its run's pending calls; its cards go once decided.
  const decideAs = async (a: Approval, accept: boolean, all: boolean) => {
    if (!onDecide || deciding.length > 0) return;
    const ids = all ? open.filter((x) => x.run === a.run).map((x) => x.id) : [a.id];
    setDeciding(ids);
    try {
      if (await onDecide(a, accept, all)) setDecided((d) => [...d, ...ids]);
    } finally {
      setDeciding([]);
    }
  };
  const tools = (as: Approval[]) => {
    const shown = as.filter((a) => !decided.includes(a.id));
    const busy = !onDecide || deciding.length > 0;
    // all of the run's calls waiting, a body edit's among them
    const pending = shown.length > 0 ? open.filter((x) => x.run === shown[0].run).length : 0;
    return (
      <>
        {pending > 1 && (
          <div className="flex items-center gap-2 px-0.5">
            <span className="text-muted-foreground text-sm">
              {t("wit.approval.count", pending)}
            </span>
            <span className="ml-auto flex gap-1.5">
              <Button
                variant="ghost"
                size="sm"
                className="font-normal text-muted-foreground"
                disabled={busy}
                onClick={() => decideAs(shown[0], false, true)}
              >
                {t("wit.approval.reject_all")}
              </Button>
              <Button
                variant="outline"
                size="sm"
                className="font-normal"
                disabled={busy}
                onClick={() => decideAs(shown[0], true, true)}
              >
                {t("wit.approval.allow_all")}
              </Button>
            </span>
          </div>
        )}
        {shown.map((a) => (
          <ToolCard
            key={a.id}
            a={a}
            busy={busy}
            decide={(accept) => decideAs(a, accept, false)}
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
        ))}
      </>
    );
  };
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
                className={cn(
                  "group/turn transition-opacity",
                  !page && m.role === "user" && i > 0 && "mt-2",
                  // what an edit replaces: the turns after the question it edits
                  editingAt >= 0 && i > editingAt && "opacity-40",
                )}
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
                    onDraft={onDraft}
                    onLink={link}
                    onCite={openCited}
                    onQuote={quoteCited}
                    onAgain={(() => {
                      const q = chat.messages[i - 1];
                      const k = q?.role === "user" ? at(q) : undefined;
                      return m === last && !locked && k !== undefined
                        ? () => again(k, textOf(q), q.metadata?.hits ?? [])
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
              <div className="flex flex-col items-center gap-3 pt-[16vh] text-center">
                <span className="flex size-12 items-center justify-center rounded-2xl border-[0.5px] bg-sidebar">
                  <RiSparkling2Line className="size-6" />
                </span>
                <h1 className="font-semibold text-xl">{t("wit.empty.title")}</h1>
                <p className="max-w-110 text-muted-foreground text-sm leading-relaxed">
                  {t("wit.empty.note")}
                </p>
                <p className="mt-3 text-muted-foreground text-sm">{t("wit.suggest")}</p>
                <div className="flex max-w-150 flex-wrap justify-center gap-2">
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
  // The context meter (once there is usage) leads the input's right-hand controls.
  const meter = usage.input > 0 || usage.history > 0;
  // Send keeps the Primary look while nothing can be sent (an empty or locked input): only a
  // disabled control would grey the whole group.
  const idle = locked || !draft.trim() || running;
  const input = (
    <InputGroup className="relative gap-2 rounded-2xl border-0 bg-background px-3 py-2.5 shadow-(--shadow-float)">
      {editing && (
        <div className="-mx-3 -mt-2.5 mb-0.5 flex items-center gap-2 rounded-t-2xl bg-muted px-3 py-1.5 text-muted-foreground text-sm">
          <RiEditLine className="size-3.5 shrink-0" />
          <span className="min-w-0 flex-1">{t("wit.editing")}</span>
          <InputGroupButton
            variant="ghost"
            size="xs"
            className="font-normal text-foreground"
            aria-label={t("wit.edit.cancel")}
            onClick={cancelEdit}
          >
            {t("common.cancel")}
            <kbd className="font-sans text-muted-foreground">Esc</kbd>
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
          // not while an input method composes: its Esc closes the candidates
          if (
            e.key === "Escape" &&
            editing &&
            !picking &&
            !e.nativeEvent.isComposing &&
            e.keyCode !== 229
          ) {
            e.preventDefault();
            return cancelEdit();
          }
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
          <Popover>
            <PopoverTrigger
              openOnHover
              render={
                <InputGroupButton
                  variant="ghost"
                  size="icon-sm"
                  className="ml-auto"
                  aria-label={t("wit.usage")}
                />
              }
            >
              <UsageRing value={usage.history} />
            </PopoverTrigger>
            <PopoverContent side="top" className="flex w-72 flex-col gap-2 text-sm">
              <span className="font-medium">
                {t("wit.usage.history", Math.min(100, Math.round(usage.history * 100)))}
              </span>
              <Progress value={Math.min(100, usage.history * 100)} />
              <span className="text-muted-foreground">{t("wit.usage.note")}</span>
              {transport.compacts && (
                <Button
                  variant="outline"
                  size="sm"
                  className="self-start font-normal"
                  disabled={compacting || running || !usage.compactable}
                  onClick={compact}
                >
                  {compacting && <RiLoaderLine className="size-3.5 animate-spin" />}
                  {t(compacting ? "wit.usage.compacting" : "wit.usage.compact")}
                </Button>
              )}
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
            </PopoverContent>
          </Popover>
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
                    "h-8 min-w-0 gap-1.5 px-2 font-normal text-sm",
                  )}
                  title={t("wit.model.pick")}
                >
                  <span className="truncate">{chosen}</span>
                  {effort?.value && (
                    <span className="text-muted-foreground">{t(`wit.effort.${effort.value}`)}</span>
                  )}
                  <RiArrowDownSLine className="size-3 text-muted-foreground" />
                </InputGroupButton>
              }
            />
            <DropdownMenuContent side="top" align="end" className="max-h-80 min-w-56">
              {!listedDefault && (
                // the default only by its name when it is none of the models listed
                <DropdownMenuRadioGroup
                  value={models.ref}
                  onValueChange={(v: string) => models.pick(v)}
                >
                  <DropdownMenuRadioItem closeOnClick value="">
                    {t("wit.model.default", model)}
                  </DropdownMenuRadioItem>
                </DropdownMenuRadioGroup>
              )}
              {[...services].map(([service, list], i) => (
                <DropdownMenuGroup key={service}>
                  {(i > 0 || !listedDefault) && <DropdownMenuSeparator />}
                  <DropdownMenuLabel>{service}</DropdownMenuLabel>
                  <DropdownMenuRadioGroup
                    // the default model is checked while the conversation follows the default
                    value={models.ref || (listedDefault?.ref ?? "")}
                    // picking the default follows it again (no model of its own)
                    onValueChange={(v: string) => models.pick(v === listedDefault?.ref ? "" : v)}
                  >
                    {list.map((c) => (
                      <DropdownMenuRadioItem closeOnClick key={c.ref} value={c.ref}>
                        {c.name}
                        {c.default && (
                          <Badge
                            variant="outline"
                            className="h-4.5 px-1.5 font-normal text-muted-foreground"
                          >
                            {t("wit.model.default_tag")}
                          </Badge>
                        )}
                      </DropdownMenuRadioItem>
                    ))}
                  </DropdownMenuRadioGroup>
                </DropdownMenuGroup>
              ))}
              {effort && (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuSub>
                    {/* the value as the item's shortcut, the trigger's arrow right by it */}
                    <DropdownMenuSubTrigger className="[&>svg]:ml-0">
                      {t("wit.effort")}
                      <DropdownMenuShortcut>
                        {t(`wit.effort.${effort.value || "default"}`)}
                      </DropdownMenuShortcut>
                    </DropdownMenuSubTrigger>
                    <DropdownMenuSubContent>
                      <DropdownMenuRadioGroup
                        value={effort.value}
                        onValueChange={(v: string) => effort.pick(v)}
                      >
                        {(["", "low", "medium", "high"] as const).map((v) => (
                          <DropdownMenuRadioItem closeOnClick key={v} value={v}>
                            {t(`wit.effort.${v || "default"}`)}
                          </DropdownMenuRadioItem>
                        ))}
                      </DropdownMenuRadioGroup>
                    </DropdownMenuSubContent>
                  </DropdownMenuSub>
                </>
              )}
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
      </div>
    );
  }
  // 调整 / 翻译: quick actions above the input; locked, they do nothing but keep their look.
  const trayButton = (Icon: typeof RiEqualizerLine, label: string) => (
    <Button
      variant="secondary"
      size="sm"
      className="h-7 gap-1.5 rounded-full px-2.5 font-normal text-xs text-foreground/80 shadow-xs hover:bg-secondary/80 disabled:opacity-100"
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
            className={cn("flex items-center gap-1.5", only ? "rounded-2xl bg-muted p-1" : "mb-2")}
          >
            {!only && (
              <DropdownMenu>
                <DropdownMenuTrigger
                  disabled={locked}
                  render={trayButton(RiEqualizerLine, t("wit.compose.adjust"))}
                />
                <DropdownMenuContent side="top" align="start" className="w-auto min-w-72 max-w-md">
                  {info.skills.map((s) => (
                    <DropdownMenuItem
                      key={s.id}
                      className="flex items-center justify-between gap-4 py-1.5"
                      onClick={() => ask(s.label, { skill: s.id, lang: "" })}
                    >
                      <span className="shrink-0 font-medium">{s.label}</span>
                      <span className="text-right text-xs text-muted-foreground whitespace-nowrap">
                        {s.hint}
                      </span>
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
              <DropdownMenuContent side="top" align="start" className="w-auto min-w-48">
                {menuLanguages.length === 0 ? (
                  <DropdownMenuItem disabled>{t("wit.compose.translate_none")}</DropdownMenuItem>
                ) : (
                  menuLanguages.map((l) => (
                    <DropdownMenuItem
                      key={l.tag}
                      onClick={() =>
                        ask(t("wit.compose.translate_to", l.label), {
                          skill: "translate-draft",
                          lang: l.tag,
                        })
                      }
                    >
                      {l.label}
                    </DropdownMenuItem>
                  ))
                )}
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
