import { useChat } from "@ai-sdk/react";
import {
  RiAddLine,
  RiAlertLine,
  RiArrowDownLine,
  RiArrowDownSLine,
  RiArrowUpLine,
  RiBox3Line,
  RiBrainLine,
  RiCheckboxCircleLine,
  RiCloseCircleLine,
  RiContractUpDownLine,
  RiDraftLine,
  RiEditBoxLine,
  RiEditLine,
  RiEqualizerLine,
  RiErrorWarningLine,
  RiExternalLinkLine,
  RiFileCopyLine,
  RiGlobalLine,
  RiLoaderLine,
  RiMagicLine,
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
import type { DynamicToolUIPart } from "ai";
import { cn } from "cn";
import {
  type FormEvent,
  Fragment,
  type ReactNode,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
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
import {
  Questionnaire,
  QuestionnaireActions,
  QuestionnaireChoice,
  QuestionnaireChoiceDescription,
  QuestionnaireChoices,
  QuestionnaireDescription,
  QuestionnaireInput,
  QuestionnaireItem,
  QuestionnaireNext,
  QuestionnairePrevious,
  QuestionnaireProgress,
  QuestionnaireSkip,
  QuestionnaireSubmit,
  QuestionnaireTitle,
} from "@/components/ui/questionnaire";
import {
  TraceCollapsible,
  TraceCollapsibleContent,
  TraceCollapsibleTrigger,
  TraceIcon,
  TraceLine,
  TraceTitle,
} from "@/components/ui/trace";
import type {
  Approval,
  Citation,
  MailHit,
  ModelChoice,
  WitCommand,
  WitHistory,
  WitInfo,
  WitUsage,
} from "@/mygo";
import { currentLocale, type T } from "@/shared/i18n";
import { Citations, type Nums } from "./Citations";
import { type Command, CommandPicker } from "./Commands";
import { MailCard, senderName } from "./MailCard";
import { type PickerKeys, QuoteChips, QuotePicker } from "./Quote";
import { Cite, citedNumbers, plainCites, Response } from "./Response";
import {
  historyToMessages,
  questionId,
  type WitMeta,
  type WitTransport,
  type WitUIMessage,
} from "./transport";

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

/**
 * How full the history budget is, as Claude Code shows its context: the share used, a bar of what
 * takes it (the conversation, its summary) and what is left, each a row with its share.
 */
function ContextUsage({ usage, t }: { usage: WitUsage; t: T }) {
  const pct = (v: number) => Math.round(Math.min(1, Math.max(0, v)) * 100);
  const summary = pct(usage.summary);
  const used = pct(usage.history);
  const rows = [
    {
      key: "conversation",
      label: t("wit.ctx.conversation"),
      value: Math.max(0, used - summary),
      tone: "bg-blue-500",
    },
    ...(summary > 0
      ? [{ key: "summary", label: t("wit.ctx.summary"), value: summary, tone: "bg-amber-500" }]
      : []),
    { key: "free", label: t("wit.ctx.free"), value: 100 - used, tone: "bg-muted" },
  ];
  return (
    <div className="flex flex-col gap-2">
      <span className="font-medium">{t("wit.ctx.used", used)}</span>
      <div className="flex h-1.5 gap-0.5 overflow-hidden rounded-full bg-muted" aria-hidden="true">
        {rows
          .filter((r) => r.key !== "free" && r.value > 0)
          .map((r) => (
            <span key={r.key} className={cn("h-full", r.tone)} style={{ width: `${r.value}%` }} />
          ))}
      </div>
      <ul className="flex flex-col gap-1">
        {rows.map((r) => (
          <li key={r.key} className="flex items-center gap-2">
            <span className={cn("size-2.5 shrink-0 rounded-[3px]", r.tone)} aria-hidden="true" />
            <span className="flex-1">{r.label}</span>
            <span className="text-muted-foreground tabular-nums">{r.value}%</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * When a message was sent, as chat apps put it under the message: its time today, else its date
 * and time (the year when not this one); the full date and time on hover.
 */
function MessageTime({ ms, className }: { ms: number; className?: string }) {
  const d = new Date(ms);
  const now = new Date();
  const locale = currentLocale();
  const opts: Intl.DateTimeFormatOptions = { hour: "2-digit", minute: "2-digit", hour12: false };
  if (d.toDateString() !== now.toDateString())
    Object.assign(opts, { month: "numeric", day: "numeric" });
  if (d.getFullYear() !== now.getFullYear()) opts.year = "numeric";
  return (
    <time
      dateTime={d.toISOString()}
      title={d.toLocaleString(locale)}
      className={cn("text-muted-foreground text-xs tabular-nums", className)}
    >
      {d.toLocaleString(locale, opts)}
    </time>
  );
}

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

/** How long an answer worked (ms) as the steps line says it: N 秒, M 分 S 秒; "" when not known. */
export const elapsed = (ms: number | undefined, t: T) => {
  if (!ms) return "";
  const s = Math.max(1, Math.round(ms / 1000));
  return s < 60
    ? t("wit.elapsed.seconds", s)
    : t("wit.elapsed.minutes", Math.floor(s / 60), s % 60);
};

const clip = (s: string, n = ARGS) => (s.length > n ? `${s.slice(0, n)}…` : s);

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
    <div className="overflow-hidden rounded-xl border-[0.5px] bg-background shadow-(--shadow-xs)">
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

const ASK_QUESTION_TOOL = "ask_user_question";

type Question = {
  question: string;
  header: string;
  /** Shown under the question, when the model gives one. */
  description?: string;
  multiSelect: boolean;
  options: { label: string; description: string }[];
  /**
   * An MCP form's field: choice (options only), text, number, integer or boolean (yes or no);
   * none for Wit's questions (options and the user's own words).
   */
  kind?: string;
  required?: boolean;
};

type Questions = {
  questions: Question[];
  answers?: string[][];
  /** For an MCP server's form: the server that asks, the round (from 1), and what it says. */
  server?: string;
  round?: number;
  message?: string;
};

/**
 * What a decision here settles: the approval, and for an MCP form its round, since the server may
 * ask again on the same approval once answered (or once its call is allowed).
 */
/** The round of a's form the user answers (0: ask_user_question's, which has no rounds). */
export const formRound = (a: Approval) => questionArgs(a).round ?? 0;

const decisionKey = (a: Approval) => (a.form ? `${a.id}:${questionArgs(a).round}` : String(a.id));

/** Whether a is a question: Wit's (ask_user_question) or an MCP server's form. */
const isQuestion = (a: Approval) => a.tool === ASK_QUESTION_TOOL || !!a.form;

/** A question's arguments (an MCP form's, else the call's), with its answers once answered; no questions when they do not parse. */
function questionArgs(a: Approval): Questions {
  try {
    const v = JSON.parse(a.form || a.args || "{}");
    if (Array.isArray(v?.questions)) return v;
  } catch {
    // not JSON: no questions to show
  }
  return { questions: [] };
}

/** A question's choices as values and labels: yes and no for a boolean field. */
function choicesOf(q: Question, t: T): { value: string; label: string; description?: string }[] {
  if (q.kind === "boolean")
    return [
      { value: "true", label: t("wit.question.yes") },
      { value: "false", label: t("wit.question.no") },
    ];
  return (q.options ?? []).map((o) => ({
    value: o.label,
    label: o.label,
    description: o.description,
  }));
}

/**
 * Questions Wit asks (ask_user_question) waiting, one at a time (the Questionnaire): each with its
 * options, one picked or several where multiSelect, or the user's own words, or 跳过 to skip it
 * (the last one's submits); 提交 sends them all, a skipped one with no answer.
 */
function QuestionCard({
  a,
  busy,
  answer,
  t,
}: {
  a: Approval;
  busy: boolean;
  /** Without it, a host that takes no answers: nothing is sent. */
  answer?(answers: string[][]): void;
  t: T;
}) {
  const { questions, server, message } = questionArgs(a);
  // the picked options and the own words are the form's values, under each question's name: an
  // option goes as written (an MCP form's must match exactly), own words trimmed
  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    answer?.(
      questions.map((q, i) =>
        form
          .getAll(`q${i}`)
          .map(String)
          .flatMap((v) =>
            q.options?.some((o) => o.label === v) ? [v] : v.trim() ? [v.trim()] : [],
          ),
      ),
    );
  };
  return (
    <div className="overflow-hidden rounded-xl border-[0.5px] bg-background shadow-(--shadow-xs)">
      <div className="flex items-center gap-2 border-b-[0.5px] bg-sidebar px-3.5 py-2.5 leading-5">
        <span className="font-semibold">
          {server ? t("wit.question.asks_mcp", server) : t("wit.question.asks")}
        </span>
        <Badge
          variant="outline"
          className="ml-auto border-(--warning-border) bg-(--warning-bg) text-(--warning-text)"
        >
          {t("wit.approval.pending")}
        </Badge>
      </div>
      {server && (
        // the server's words: plain text, never markup
        <div className="flex flex-col gap-1 border-b-[0.5px] px-3.5 py-2.5 text-md">
          {message && <p className="whitespace-pre-wrap">{message}</p>}
          <p className="text-muted-foreground text-sm">{t("wit.question.mcp_notice", server)}</p>
        </div>
      )}
      <Questionnaire onSubmit={submit} className="px-3.5 py-3 text-md">
        {questions.length > 1 && (
          <QuestionnaireProgress
            render={(props, s) => {
              const text = t("wit.question.progress", s.current, s.total);
              return (
                <div
                  {...props}
                  role="progressbar"
                  aria-label={t("wit.question.asks")}
                  aria-valuetext={text}
                >
                  {text}
                </div>
              );
            }}
          />
        )}
        {questions.map((q, i) => (
          <QuestionnaireItem key={q.question} name={`q${i}`} multiple={q.multiSelect}>
            <QuestionnaireTitle className="flex flex-wrap items-center gap-2">
              {q.header && <Badge variant="secondary">{q.header}</Badge>}
              {q.question}
              {q.multiSelect && (
                <span className="font-normal text-muted-foreground text-sm">
                  {t("wit.question.multi")}
                </span>
              )}
            </QuestionnaireTitle>
            {q.description && <QuestionnaireDescription>{q.description}</QuestionnaireDescription>}
            {choicesOf(q, t).length > 0 && (
              <QuestionnaireChoices>
                {choicesOf(q, t).map((o) => (
                  <QuestionnaireChoice key={o.value} value={o.value}>
                    {o.label}
                    {o.description && (
                      <QuestionnaireChoiceDescription>
                        {o.description}
                      </QuestionnaireChoiceDescription>
                    )}
                  </QuestionnaireChoice>
                ))}
              </QuestionnaireChoices>
            )}
            {/* a form's choice takes only its choices */}
            {q.kind !== "choice" && q.kind !== "boolean" && (
              <QuestionnaireInput
                aria-label={q.kind ? q.question : t("wit.question.other")}
                placeholder={q.kind ? undefined : t("wit.question.other_placeholder")}
                inputMode={q.kind === "number" || q.kind === "integer" ? "decimal" : undefined}
              />
            )}
          </QuestionnaireItem>
        ))}
        <QuestionnaireActions>
          <QuestionnairePrevious>{t("wit.question.previous")}</QuestionnairePrevious>
          <QuestionnaireSkip disabled={busy}>{t("wit.question.skip")}</QuestionnaireSkip>
          <QuestionnaireNext>{t("wit.question.next")}</QuestionnaireNext>
          <QuestionnaireSubmit disabled={busy || !answer}>
            {t("wit.question.submit")}
          </QuestionnaireSubmit>
        </QuestionnaireActions>
      </Questionnaire>
    </div>
  );
}

/** A decided approval as one line (已允许 / 已拒绝 / 没有完成 · what), a draft Wit made as its card. */
function Settled({ a, onDraft, t }: { a: Approval; onDraft?(id: number): Promise<void>; t: T }) {
  const label = toolLabel(a.tool, t);
  // the draft sent or deleted since this history was read: opening it fails, and its card goes
  const [gone, setGone] = useState(false);
  // a question: each one with its answer, or 未回答 (skipped, or the lot skipped)
  if (isQuestion(a) && a.state !== "failed") {
    const { questions, answers } = questionArgs(a);
    const done = a.state === "done";
    return (
      questions.length > 0 && (
        <dl className="flex flex-col gap-1.5 rounded-xl border-[0.5px] bg-background px-3 py-2.5 text-md">
          {questions.map((q, i) => (
            <div key={q.question}>
              <dt className="text-muted-foreground text-sm">{q.question}</dt>
              <dd>
                {done && answers?.[i]?.length ? (
                  answers[i]
                    .map((v) => choicesOf(q, t).find((o) => o.value === v)?.label ?? v)
                    .join(", ")
                ) : (
                  <span className="text-muted-foreground">{t("wit.question.unanswered")}</span>
                )}
              </dd>
            </div>
          ))}
        </dl>
      )
    );
  }
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

// A heading line of a thought: "# Title", or "**Title**" / "__Title__" alone (how reasoning
// summaries head their parts).
const HEADING = /^\s*(?:#{1,6}\s+(.+?)[\s#]*|\*\*([^*\n]+)\*\*|__([^_\n]+)__)\s*$/;

/** Markdown's marks out of a line: what it reads as, on one line. */
const plain = (s: string) =>
  s
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\*\*|__|`/g, "")
    .replace(/^\s*(?:#{1,6}|>|[-*+]|\d+\.)\s+/, "")
    .replace(/\s+/g, " ")
    .trim();

/**
 * What a thought is about, as its line names it ("思考 · …"), as agents do: its latest heading
 * while it streams; once done, its first heading, else its first sentence. "" for none.
 */
export function thoughtTitle(text: string, streaming: boolean): string {
  const heads = text.split("\n").flatMap((l) => {
    const m = HEADING.exec(l);
    const h = m && plain(m[1] ?? m[2] ?? m[3]);
    return h ? [h] : [];
  });
  if (streaming || heads.length > 0) return (streaming ? heads.at(-1) : heads[0]) ?? "";
  const first = plain(text.split("\n").find((l) => plain(l)) ?? "");
  return /^.*?(?:[。！？]|[.!?](?=\s|$))/.exec(first)?.[0] ?? first;
}

/**
 * A reasoning part: "思考中 · its latest heading" while it streams, open; "思考 · its title" once
 * done, folded (the user may open it again). The line keeps to one line; hovering shows the title.
 */
function Thought({ text, streaming, t }: { text: string; streaming: boolean; t: T }) {
  const [open, setOpen] = useState(streaming);
  useEffect(() => setOpen(streaming), [streaming]);
  const title = thoughtTitle(text, streaming);
  return (
    <TraceCollapsible open={open} onOpenChange={setOpen}>
      <TraceCollapsibleTrigger>
        <TraceLine status={streaming ? "running" : undefined}>
          <TraceIcon>
            <RiBrainLine />
          </TraceIcon>
          <TraceTitle title={title || undefined}>
            {t(streaming ? "wit.thinking_now" : "wit.reasoning")}
            {title && ` · ${clip(title, 80)}`}
          </TraceTitle>
        </TraceLine>
      </TraceCollapsibleTrigger>
      <TraceCollapsibleContent className="whitespace-pre-wrap text-sm">
        {text}
      </TraceCollapsibleContent>
    </TraceCollapsible>
  );
}

/**
 * What an answer did before its final text, as one line ("已完成，用时 42 秒"): open while the
 * answer works (处理中), folded once it is done, as agents fold their steps under their answer.
 */
function Work({ label, live, children }: { label: string; live: boolean; children: ReactNode }) {
  const [open, setOpen] = useState(live);
  useEffect(() => setOpen(live), [live]);
  return (
    <TraceCollapsible open={open} onOpenChange={setOpen}>
      <TraceCollapsibleTrigger>
        <TraceLine status={live ? "running" : undefined}>
          <TraceTitle>{label}</TraceTitle>
        </TraceLine>
      </TraceCollapsibleTrigger>
      <TraceCollapsibleContent className="flex flex-col gap-2.5">
        {children}
      </TraceCollapsibleContent>
    </TraceCollapsible>
  );
}

/**
 * A call as a line of its answer: what it does, its query and who sent the mail it found (one by
 * name, several as the first +N, unfolding into a chip for each mail); hovering a sender shows its
 * mail's card (card). Spinning while it runs.
 */
function ToolLine({
  p,
  running,
  cites,
  nums,
  card,
  t,
}: {
  p: DynamicToolUIPart;
  running: boolean;
  cites: Citation[];
  nums: Nums;
  /** The card of mail n, as a citation's popover shows it. */
  card(n: number): ReactNode;
  t: T;
}) {
  const Icon = running ? RiLoaderLine : stepIcon(p.toolName);
  const query =
    typeof (p.input as { query?: unknown })?.query === "string"
      ? (p.input as { query: string }).query
      : "";
  const found = cites.filter((c) => c.callIds.includes(p.toolCallId));
  const senders = [...new Set(found.map((c) => senderName(c.from)))];
  // a found mail's sender, its card on hover
  const sender = (c: Citation, className: string) => {
    const n = nums.get(c.messageId)?.n ?? c.n;
    return (
      <Cite key={c.messageId} n={n} card={card(n)} className={className}>
        {senderName(c.from)}
      </Cite>
    );
  };
  const line = (
    <TraceLine
      status={running ? "running" : p.state === "output-error" ? "error" : undefined}
      title={p.toolName}
    >
      <TraceIcon>
        <Icon className={cn(running && "animate-spin")} />
      </TraceIcon>
      <TraceTitle>{toolLabel(p.toolName, t)}</TraceTitle>
      {query && (
        <span className="min-w-0 truncate rounded-sm bg-muted px-1.5 font-mono text-xs">
          {clip(query)}
        </span>
      )}
      {found.length === 1 ? (
        <span className="flex min-w-0 shrink-0 items-center gap-1">
          ·
          {sender(
            found[0],
            "min-w-0 cursor-pointer truncate hover:text-foreground hover:underline",
          )}
        </span>
      ) : (
        senders.length > 0 && (
          <span className="min-w-0 shrink-0 truncate">
            · {senders[0]}
            {senders.length > 1 && ` +${senders.length - 1}`}
          </span>
        )
      )}
    </TraceLine>
  );
  if (found.length < 2) return line;
  return (
    <TraceCollapsible>
      <TraceCollapsibleTrigger>{line}</TraceCollapsibleTrigger>
      <TraceCollapsibleContent className="flex flex-wrap gap-1">
        {found.map((c) =>
          sender(
            c,
            "cursor-pointer rounded-full border-[0.5px] px-2 text-muted-foreground text-xs leading-5 hover:text-foreground data-popup-open:bg-muted data-popup-open:text-foreground",
          ),
        )}
      </TraceCollapsibleContent>
    </TraceCollapsible>
  );
}

/**
 * A run of calls of one tool, as one line that unfolds into a line for each: "读取了 7 封邮件" where
 * the tool has its count's wording, else its name ×7.
 */
function ToolGroup({
  ps,
  going,
  cites,
  nums,
  card,
  t,
}: {
  ps: DynamicToolUIPart[];
  going(p: DynamicToolUIPart): boolean;
  cites: Citation[];
  nums: Nums;
  card(n: number): ReactNode;
  t: T;
}) {
  const name = ps[0].toolName;
  const key = `wit.tools.${name}`;
  const label =
    t(key, ps.length) !== key ? t(key, ps.length) : `${toolLabel(name, t)} ×${ps.length}`;
  const running = ps.some(going);
  const Icon = running ? RiLoaderLine : stepIcon(name);
  return (
    <TraceCollapsible>
      <TraceCollapsibleTrigger>
        <TraceLine
          status={
            running ? "running" : ps.some((p) => p.state === "output-error") ? "error" : undefined
          }
        >
          <TraceIcon>
            <Icon className={cn(running && "animate-spin")} />
          </TraceIcon>
          <TraceTitle>{label}</TraceTitle>
        </TraceLine>
      </TraceCollapsibleTrigger>
      <TraceCollapsibleContent className="flex flex-col gap-2">
        {ps.map((p) => (
          <ToolLine
            key={p.toolCallId}
            p={p}
            running={going(p)}
            cites={cites}
            nums={nums}
            card={card}
            t={t}
          />
        ))}
      </TraceCollapsibleContent>
    </TraceCollapsible>
  );
}

/** Whether approval a answers the call of part id (a skill's has a part ID of its own: partId). */
const answers = (a: Approval, id: string) => a.callId === id || `${a.callId}-${a.id}` === id;

/**
 * An answer, its parts in the order they came: what it thought (open while it thinks), each call as
 * a line (or, where it waits on the user or the user decided it, its card), and what it wrote.
 */
function Answer({
  m,
  live,
  nums,
  approvals,
  review,
  decisions,
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
  decisions?: Record<number, "applied" | "rejected" | "adjusted">;
  /** A call's approval waiting for the user, as its card (the run's first with the run's header). */
  tools: (as: Approval[]) => ReactNode;
  onDraft?(approval: number): Promise<void>;
  onLink(url: string): void;
  onCite?(id: number): void;
  /** Adds a mail to the input's quoted mail (引用邮件). */
  onQuote(h: MailHit): void;
  /** Asks the answer's question again in its place (重新生成), where it can be. */
  onAgain?(): void;
  /** What shows while the answer is live and nothing of it moves: the working line. */
  working: ReactNode;
  page: boolean;
  t: T;
}) {
  // a message found gone since (opening it failed) is gone in every answer that cites it
  const cites = citesOf(m).map((c) => (nums.get(c.messageId)?.c.gone ? { ...c, gone: true } : c));
  const [sources, setSources] = useState(false);
  const text = m.parts
    .flatMap((p) => (p.type === "text" && p.text.trim() ? [p.text] : []))
    .join("\n\n");
  // the mail its text cites, what 引用了 N 封邮件 lists: not all the mail it read
  const ns = citedNumbers(text);
  const cited = cites.filter((c) => ns.has(nums.get(c.messageId)?.n ?? c.n));
  // The calls under way: no result yet and none waiting on the user.
  const going = (p: DynamicToolUIPart) =>
    live && (p.state === "input-streaming" || p.state === "input-available");
  // Its own calls the user decided (draft edits have their own rows), as they stand now: an
  // answer's approvals are its run's (a call ID alone may be another run's).
  const settled = m.parts
    .flatMap((p) => (p.type === "data-approvals" ? p.data : []))
    .map((a) => approvals.get(String(a.id)) ?? a)
    .filter(
      (a, i, all) =>
        all.findIndex((x) => x.id === a.id) === i &&
        ["done", "failed", "rejected", "stale"].includes(a.state) &&
        !a.draftEdit,
    );
  const calls = m.parts.filter((p): p is DynamicToolUIPart => p.type === "dynamic-tool");
  const requested = (p: DynamicToolUIPart) =>
    p.state === "approval-requested" ? approvals.get(p.approval.id) : undefined;
  const isDraftEdit = (tool: string) => tool === "update_draft" || tool === "update_selection";
  const hasNoChange =
    calls.some((c) => {
      if (!isDraftEdit(c.toolName)) return false;
      const s =
        (c.state === "output-available" && (c.output as any)?.summary) ||
        (c.state === "approval-requested" && approvals.get(c.approval?.id ?? "")?.summary);
      return s === "no_change";
    }) ||
    m.parts.some(
      (p) =>
        p.type === "data-approvals" &&
        p.data.some((a) => (approvals.get(String(a.id)) ?? a).summary === "no_change"),
    );

  const hasAdjusted =
    !hasNoChange &&
    (calls.some((c) => {
      if (!isDraftEdit(c.toolName)) return false;
      if (c.approval?.id && decisions?.[Number(c.approval.id)] === "adjusted") return true;
      const a = c.approval?.id ? approvals.get(c.approval.id) : undefined;
      return Boolean(a?.id && decisions?.[a.id] === "adjusted");
    }) ||
    m.parts.some(
      (p) =>
        p.type === "data-approvals" &&
        p.data.some((a) => isDraftEdit(a.tool) && decisions?.[a.id] === "adjusted"),
    ));

  const hasInitial =
    !hasNoChange &&
    !hasAdjusted &&
    (calls.some((c) => {
      if (!isDraftEdit(c.toolName)) return false;
      const s =
        (c.state === "output-available" && (c.output as any)?.summary) ||
        (c.approval?.id && approvals.get(c.approval.id)?.summary);
      return s === "initial";
    }) ||
    m.parts.some(
      (p) =>
        p.type === "data-approvals" &&
        p.data.some(
          (a) =>
            isDraftEdit(a.tool) && (approvals.get(String(a.id)) ?? a).summary === "initial",
        ),
    ));

  const hasApplied =
    !hasNoChange &&
    !hasAdjusted &&
    !hasInitial &&
    (calls.some((c) => {
      if (!isDraftEdit(c.toolName)) return false;
      if (c.approval?.id && decisions?.[Number(c.approval.id)] === "adjusted") return false;
      if (c.approval?.id && decisions?.[Number(c.approval.id)] === "applied") return true;
      const a = c.approval?.id ? approvals.get(c.approval.id) : undefined;
      if (a?.id && decisions?.[a.id] === "adjusted") return false;
      if (c.state === "output-available") {
        const out = c.output as any;
        if (out?.summary === "no_change" || out?.no_change || out?.summary === "initial") return false;
        return true;
      }
      return (
        (a?.id && decisions?.[a.id] === "applied") ||
        (a?.state === "done" && a?.summary !== "no_change" && a?.summary !== "initial")
      );
    }) ||
    m.parts.some(
      (p) =>
        p.type === "data-approvals" &&
        p.data.some(
          (a) =>
            isDraftEdit(a.tool) &&
            (decisions?.[a.id] === "applied" ||
              (decisions?.[a.id] !== "adjusted" &&
                a.state === "done" &&
                a.summary !== "no_change" &&
                a.summary !== "initial")),
        ),
    ));

  const hasRejected =
    !hasNoChange &&
    !hasApplied &&
    !hasAdjusted &&
    !hasInitial &&
    (calls.some((c) => {
      if (!isDraftEdit(c.toolName)) return false;
      if (c.approval?.id && decisions?.[Number(c.approval.id)] === "rejected") return true;
      if (c.state === "output-denied") return true;
      const a = c.approval?.id ? approvals.get(c.approval.id) : undefined;
      return (
        (a?.id && decisions?.[a.id] === "rejected") ||
        a?.state === "rejected" ||
        a?.state === "stale"
      );
    }) ||
    m.parts.some(
      (p) =>
        p.type === "data-approvals" &&
        p.data.some(
          (a) =>
            isDraftEdit(a.tool) &&
            (decisions?.[a.id] === "rejected" || a.state === "rejected" || a.state === "stale"),
        ),
    ));
  // Calls of one tool in a row, each a plain line (no card), go as one line: each run by its first.
  const runs = new Map<number, DynamicToolUIPart[]>();
  let run: DynamicToolUIPart[] | undefined;
  m.parts.forEach((p, i) => {
    if (
      p.type === "dynamic-tool" &&
      !requested(p) &&
      !settled.some((a) => answers(a, p.toolCallId))
    ) {
      if (run?.[0].toolName === p.toolName) run.push(p);
      else {
        run = [p];
        runs.set(i, run);
      }
    } else if (
      p.type === "dynamic-tool" ||
      (p.type === "text" && p.text.trim()) ||
      (p.type === "reasoning" && p.text)
    )
      run = undefined; // what shows between breaks the run (data parts show nothing)
  });
  // a call with a card (waiting, or decided) shows it, never folded away
  const carded = (p: (typeof m.parts)[number]) =>
    p.type === "dynamic-tool" && (!!requested(p) || settled.some((a) => answers(a, p.toolCallId)));
  // What it did before its final text (the text after its last step) folds into one line per
  // stretch between cards, so a card keeps its place among the steps, when the stretch is more
  // than one line.
  const end =
    m.parts.findLastIndex(
      (p) => p.type === "dynamic-tool" || (p.type === "reasoning" && !!p.text),
    ) + 1;
  const stretches: number[][] = [[]];
  m.parts.forEach((p, i) => {
    if (i >= end) return;
    if (carded(p)) stretches.push([]);
    else stretches[stretches.length - 1].push(i);
  });
  // what shows a line: a call (a run's first), a thought, text; data parts show nothing
  const shows = (i: number) => {
    const p = m.parts[i];
    if (p.type === "dynamic-tool") return runs.has(i);
    return (p.type === "text" || p.type === "reasoning") && !!p.text.trim();
  };
  // a stretch with a step to name it folds; text alone stays as it is
  const folds = stretches.filter(
    (w) => w.filter(shows).length > 1 && w.some((i) => m.parts[i].type !== "text" && shows(i)),
  );
  const took = !live && elapsed(m.metadata?.elapsed, t);
  // the answer's time goes on its last folded line, nearest the answer
  const timed = folds.at(-1);
  const workLabel = (work: number[]) =>
    live
      ? t("wit.work_running")
      : took && work === timed
        ? t("wit.work_done_in", took)
        : t("wit.work_done");
  // a decided approval with no call of this answer's parts (none expected) still shows, at the end
  const loose = settled.filter((a) => !calls.some((p) => answers(a, p.toolCallId)));
  // Something of it moves (text or thought streaming, a call spinning): no working line then, but
  // for a body edit's call, which the line names (正在写入正文).
  const moving = m.parts.some(
    (p) =>
      ((p.type === "text" || p.type === "reasoning") && p.state === "streaming") ||
      (p.type === "dynamic-tool" &&
        going(p) &&
        p.toolName !== "update_draft" &&
        p.toolName !== "update_selection"),
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
  // A part as it shows: a thought, text, a call's line (a run's first: the run) or its card.
  const part = (p: (typeof m.parts)[number], i: number): ReactNode => {
    if (p.type === "reasoning")
      return (
        p.text && (
          <Thought key={i} text={p.text} streaming={live && p.state === "streaming"} t={t} />
        )
      );
    if (p.type === "text")
      return (
        p.text.trim() && (
          <Response
            key={i}
            text={p.text}
            streaming={live && p.state === "streaming"}
            size={page ? "page" : "panel"}
            t={t}
            onLink={onLink}
            confirm={!page}
            cite={(n) => mail(n, false)}
            card={(n) => mail(n, true)}
          />
        )
      );
    if (p.type !== "dynamic-tool") return null;
    const a = requested(p);
    if (a)
      return (
        <Fragment key={p.toolCallId}>
          {a.id === review?.approval && (
            <div className="overflow-hidden rounded-xl border border-border bg-card shadow-md">
              <div className="flex items-center justify-between border-b border-border/40 bg-muted px-3.5 py-2.5 leading-5">
                <div className="flex items-center gap-2">
                  <span className="font-semibold text-sm">{review?.title ?? t("wit.compose.review")}</span>
                  <span className="font-mono text-foreground/80 text-sm">{a.tool}</span>
                </div>
                {review?.onToggleAdjust && (
                  <Button
                    size="icon-xs"
                    variant="ghost"
                    className="size-6 text-muted-foreground hover:text-foreground cursor-pointer"
                    onClick={review.onToggleAdjust}
                    title={t("wit.compose.adjust")}
                    aria-label={t("wit.compose.adjust")}
                  >
                    <RiEditLine className="size-3.5" />
                  </Button>
                )}
              </div>
              <div className="bg-card px-3.5 py-2.5 text-md text-foreground">{review?.summary}</div>
              {review?.adjustArea}
              <div className="flex gap-2 border-t border-border/40 bg-card px-3.5 py-2.5">
                {review?.actions}
              </div>
            </div>
          )}
          {!a.draftEdit && tools([a])}
        </Fragment>
      );
    const done = settled.find((x) => answers(x, p.toolCallId));
    if (done) return <Settled key={p.toolCallId} a={done} onDraft={onDraft} t={t} />;
    const ps = runs.get(i);
    if (!ps) return null; // a later call of a run: its first shows it
    return ps.length > 1 ? (
      <ToolGroup
        key={p.toolCallId}
        ps={ps}
        going={going}
        cites={cites}
        nums={nums}
        card={(n) => mail(n, false)}
        t={t}
      />
    ) : (
      <ToolLine
        key={p.toolCallId}
        p={p}
        running={going(p)}
        cites={cites}
        nums={nums}
        card={(n) => mail(n, false)}
        t={t}
      />
    );
  };
  return (
    <div data-role="assistant" className="flex flex-col gap-2.5">
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
      {m.parts.map((p, i) => {
        const w = folds.find((w) => w.includes(i));
        if (!w) return part(p, i);
        return (
          w[0] === i && (
            // biome-ignore lint/suspicious/noArrayIndexKey: an answer's parts only grow
            <Work key={`work${i}`} label={workLabel(w)} live={live}>
              {w.map((k) => part(m.parts[k], k))}
            </Work>
          )
        );
      })}
      {loose.map((a) => (
        <Settled key={a.id} a={a} onDraft={onDraft} t={t} />
      ))}
      {hasNoChange && (
        <div className="flex items-center gap-2 rounded-lg bg-sidebar px-3 py-2 text-base text-foreground">
          <RiSparkling2Line className="size-4 shrink-0 text-foreground" />
          <span>{t("wit.compose.no_changes")}</span>
        </div>
      )}
      {hasInitial && (
        <div className="flex items-center gap-2 rounded-lg bg-sidebar px-3 py-2 text-base text-foreground">
          <RiSparkling2Line className="size-4 shrink-0 text-foreground" />
          <span>{t("wit.compose.initial_status")}</span>
        </div>
      )}
      {hasAdjusted && (
        <div className="flex items-center gap-2 rounded-lg bg-sidebar px-3 py-2 text-base text-foreground">
          <RiSparkling2Line className="size-4 shrink-0 text-foreground" />
          <span>{t("wit.compose.adjusting_status")}</span>
        </div>
      )}
      {hasApplied && (
        <div className="flex items-center gap-2 rounded-lg bg-sidebar px-3 py-2 text-base text-foreground">
          <RiSparkling2Line className="size-4 shrink-0 text-foreground" />
          <span>{t("wit.compose.applied_status")}</span>
        </div>
      )}
      {hasRejected && (
        <div className="flex items-center gap-2 rounded-lg bg-sidebar px-3 py-2 text-base text-foreground">
          <RiSparkling2Line className="size-4 shrink-0 text-foreground" />
          <span>{t("wit.compose.rejected_status")}</span>
        </div>
      )}
      {live && !moving && working}
      {((text && !live) || (cited.length > 0 && onCite)) && (
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
                  (navigator.clipboard?.writeText(plainCites(text)) ?? Promise.reject()).then(
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
          {cited.length > 0 && onCite && (
            <>
              {text && !live && <span aria-hidden="true" className="mx-1.5 h-3.5 w-px bg-border" />}
              <Button
                variant="ghost"
                size="xs"
                className="font-normal text-muted-foreground"
                aria-expanded={sources}
                onClick={() => setSources((o) => !o)}
              >
                {t("wit.citations.count", cited.length)}
                <RiArrowDownSLine className={cn("transition-transform", sources && "rotate-180")} />
              </Button>
            </>
          )}
          {m.metadata?.time && !live && <MessageTime ms={m.metadata.time} className="ml-auto" />}
        </div>
      )}
      {cited.length > 0 && onCite && sources && (
        // a long list scrolls in place (an answer may have read hundreds of mails)
        <Citations
          items={cited}
          nums={nums}
          onOpen={onCite}
          t={t}
          className="max-h-96 overflow-y-auto overscroll-contain"
        />
      )}
      {cited.length > 0 && !onCite && (
        <div className="flex flex-wrap gap-1">
          {cited.map((c) => (
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
    </div>
  );
}

/** The edit the page reviews: its card in the panel holds its summary, then its decisions. */
export type PanelReview = {
  approval: number;
  summary: ReactNode;
  actions: ReactNode;
  title?: string;
  onToggleAdjust?: () => void;
  adjustArea?: ReactNode;
};

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
  decisions,
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
  onAnswer,
  skills,
  onNew,
}: {
  info?: WitInfo;
  history: WitHistory;
  locked: boolean;
  review?: PanelReview;
  decisions?: Record<number, "applied" | "rejected" | "adjusted">;
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
  /** Answers question a (ask_user_question), one list per question; true once answered. */
  onAnswer?(a: Approval, answers: string[][]): Promise<boolean>;
  /** The skills a question may name with / (the Wit page): the / menu lists them. */
  skills?(): Promise<{ name: string; description: string }[]>;
  /** Shows a new conversation (the / menu's 新对话; the Wit page). */
  onNew?(): void;
}) {
  // A command's question: the skill as its menu names it (翻译成…, or the adjustment).
  const commandLabel = (c: WitCommand) => {
    if (c.skill === "translate-draft") {
      const l = translateMenu(info?.languages ?? [], currentLocale()).find((x) => x.tag === c.lang);
      return t("wit.compose.translate_to", l?.label ?? c.lang);
    }
    return info?.skills.find((x) => x.id === c.skill)?.label ?? t("wit.compose.adjust");
  };
  const labelRef = useRef(commandLabel);
  labelRef.current = commandLabel;
  // The answer a live run goes on with (its own, the last) is taken out: the resumed stream
  // rebuilds it, then appends.
  const [start] = useState(() => {
    const messages = historyToMessages(history, commandLabel);
    const open =
      history.running && transport.owns(messages.at(-1), history.running)
        ? messages.pop()
        : undefined;
    return { messages, open };
  });
  const [deciding, setDeciding] = useState<number[]>([]);
  // decisionKey's of the approvals decided here
  const [decided, setDecided] = useState<string[]>([]);
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
  // The question being edited: sending asks it in place of the one of turn at.
  const [editing, setEditing] = useState<{ at: string }>();
  // what the input holds now, for a submit whose checks answer later
  const latest = useRef({ draft, quoted, editing });
  latest.current = { draft, quoted, editing };
  // What asking in place took off the conversation shown: back if the host refuses the question.
  const shelved = useRef<WitUIMessage[]>([]);
  // What the conversation took, as last read (a run's end reads it again).
  const [usage, setUsage] = useState(history.usage);
  const [compacting, setCompacting] = useState(false);
  // a compaction done here: said in the conversation until the next question
  const [compacted, setCompacted] = useState(false);
  // The picker: from the + menu (at -1), or from an @ at draft[at] with what follows it as query.
  const [picking, setPicking] = useState<{ at: number; query: string }>();
  const textarea = useRef<HTMLTextAreaElement>(null);
  const pickerKeys = useRef<PickerKeys>(null);
  // The / menu: open while the input starts with a / and a word, its query that word.
  const [slash, setSlash] = useState<string>();
  const commandKeys = useRef<PickerKeys>(null);
  const [skillList, setSkillList] = useState<{ name: string; description: string }[]>();
  const full = quoted.length >= MAX_QUOTED;
  const running = chat.status === "submitted" || chat.status === "streaming";
  const { resumeStream } = chat;
  const locale = currentLocale();
  const menuLanguages = useMemo(
    () => translateMenu(info?.languages ?? [], locale),
    [info?.languages, locale],
  );
  // the skills, read once the / menu first opens; the models, each time it opens
  useEffect(() => {
    if (slash === undefined || skillList || !skills) return;
    skills().then(setSkillList, () => setSkillList([]));
  }, [slash, skillList, skills]);
  const slashOpen = slash !== undefined;
  // biome-ignore lint/correctness/useExhaustiveDependencies: on opening only
  useEffect(() => {
    if (slashOpen && models) models.list().then(setChoices, () => {});
  }, [slashOpen]);

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
        if (!h.turns.some((x) => x.runs.includes(run))) {
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
          setMessages(historyToMessages(h, labelRef.current));
          putBackRef.current(q);
          return;
        }
        setMessages(historyToMessages(h, labelRef.current));
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
    setCompacted(false);
    void chat.sendMessage({ text, metadata: { ...metadata, time: Date.now() } });
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
  // A question's turn, when the host takes it again now (core's say, as last read); none for one not
  // read back yet.
  const at = (m: WitUIMessage) =>
    transport.rewinds && m.metadata?.canAskAgain ? m.metadata.turn : undefined;
  // Asks again in place of the question of turn i: what came from it on goes, here and in the host.
  const again = (i: string, text: string, hits: MailHit[]) => {
    if (locked || running) return;
    const k = chat.messages.findIndex((m) => m.id === questionId(i));
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
  const editingAt = editing ? chat.messages.findIndex((m) => m.id === questionId(editing.at)) : -1;
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
    const head = value.slice(0, caret);
    setSlash(/^\/\S*$/.test(head) ? head.slice(1) : undefined);
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
    setCompacted(false);
    transport
      .compact()
      .then(() => {
        setCompacted(true);
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
    (a) => a.state === "pending" && !decided.includes(decisionKey(a)),
  );
  // Decides a, or with all its run's pending calls; its cards go once decided.
  const decideAs = async (a: Approval, accept: boolean, all: boolean) => {
    if (!onDecide || deciding.length > 0) return;
    // 全部允许 answers no question: those stay
    const ids = all
      ? open.filter((x) => x.run === a.run && !(accept && isQuestion(x))).map((x) => x.id)
      : [a.id];
    setDeciding(ids);
    try {
      if (await onDecide(a, accept, all))
        setDecided((d) => [...d, ...open.filter((x) => ids.includes(x.id)).map(decisionKey)]);
    } finally {
      setDeciding([]);
    }
  };
  const answerAs = async (a: Approval, answers: string[][]) => {
    if (!onAnswer || deciding.length > 0) return;
    setDeciding([a.id]);
    try {
      if (await onAnswer(a, answers)) setDecided((d) => [...d, decisionKey(a)]);
    } finally {
      setDeciding([]);
    }
  };
  const tools = (as: Approval[]) => {
    const shown = as.filter((a) => !decided.includes(decisionKey(a)));
    const busy = !onDecide || deciding.length > 0;
    // all of the run's calls waiting, a body edit's among them
    // 全部允许 answers no question: questions are not counted
    const pending =
      shown.length > 0 ? open.filter((x) => x.run === shown[0].run && !isQuestion(x)).length : 0;
    // the header goes over the run's first card still waiting (a body edit has none)
    const head =
      pending > 1 &&
      open.find((x) => x.run === shown[0].run && !isQuestion(x) && !x.draftEdit)?.id ===
        shown[0].id;
    return (
      <>
        {head && (
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
        {shown.map((a) =>
          isQuestion(a) ? (
            <QuestionCard
              // by round: a form asked again on the approval starts with no answers, at its first
              key={decisionKey(a)}
              a={a}
              busy={busy}
              answer={onAnswer && ((answers) => answerAs(a, answers))}
              t={t}
            />
          ) : (
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
                        if (await onSession(a))
                          setDecided((d) => [
                            ...d,
                            ...open.filter((x) => ids.includes(x.id)).map(decisionKey),
                          ]);
                      } finally {
                        setDeciding([]);
                      }
                    }
                  : undefined
              }
              t={t}
            />
          ),
        )}
      </>
    );
  };
  const page = layout === "page";
  // The page's column (max-w-202): 760 of text inside its 24 padding, as the design's.
  // A run going: its working line says Wit writes the body while a body edit's call is in (no
  // result yet), else that it thinks; Stop is the input's.
  const writes =
    last?.role === "assistant" &&
    last.parts.some(
      (p) =>
        p.type === "dynamic-tool" &&
        (p.toolName === "update_draft" || p.toolName === "update_selection") && // a streamed call: no approval says it yet
        (p.state === "input-streaming" || p.state === "input-available"),
    );
  const working = (
    <TraceLine status="running" role="status">
      <TraceTitle>{t(writes ? "wit.compose.working" : "wit.thinking")}</TraceTitle>
    </TraceLine>
  );
  // The / menu's commands: the skills a question may name, 调整 / 翻译 (the compose window), and
  // what the conversation can do, each with what it stands at: 压缩 its usage, 思考强度 and 模型
  // their choices, opened in the menu. A skill is put in the input to ask with; the rest run at once.
  const session = t("wit.cmd.session");
  const now = (f: () => void) => () => {
    setSlash(undefined);
    setDraft("");
    f();
  };
  const commands: Command[] = [
    ...(skillList ?? []).map((sk) => ({
      key: `skill:${sk.name}`,
      group: t("wit.cmd.skills"),
      icon: <RiMagicLine />,
      label: `/${sk.name}`,
      hint: sk.description,
      run: () => {
        setSlash(undefined);
        setDraft(`/${sk.name} `);
      },
    })),
    ...(info && !info.translateOnly
      ? info.skills.map((sk) => ({
          key: `adjust:${sk.id}`,
          group: t("wit.compose.adjust"),
          icon: <RiEqualizerLine />,
          label: sk.label,
          hint: sk.hint,
          run: now(() => ask(sk.label, { skill: sk.id, lang: "" })),
        }))
      : []),
    ...(info
      ? menuLanguages.map((l) => ({
          key: `translate:${l.tag}`,
          group: t("wit.compose.translate"),
          icon: <RiTranslate2 />,
          label: t("wit.compose.translate_to", l.label),
          run: now(() =>
            ask(t("wit.compose.translate_to", l.label), { skill: "translate-draft", lang: l.tag }),
          ),
        }))
      : []),
    ...(onNew
      ? [
          {
            key: "new",
            group: session,
            icon: <RiEditBoxLine />,
            label: t("ask.new"),
            run: now(onNew),
          },
        ]
      : []),
    ...(transport.compacts
      ? [
          {
            key: "compact",
            group: session,
            icon: <UsageRing value={usage.history} />,
            label: t("wit.cmd.compact"),
            // nothing to condense yet: greyed, saying so, as the meter's button is
            disabled: !usage.compactable,
            hint: usage.compactable
              ? t("wit.cmd.compact_hint", Math.min(100, Math.round(usage.history * 100)))
              : t("error.nothing_to_compact"),
            run: now(() => !compacting && !running && compact()),
          },
        ]
      : []),
    ...(effort
      ? [
          {
            key: "effort",
            group: session,
            icon: <RiBrainLine />,
            label: t("wit.effort"),
            value: t(`wit.effort.${effort.value || "default"}`),
            options: (["", "low", "medium", "high"] as const).map((v) => ({
              key: `effort:${v}`,
              group: session,
              label: t(`wit.effort.${v || "default"}`),
              checked: effort.value === v,
              run: now(() => effort.pick(v)),
            })),
          },
        ]
      : []),
    ...(model && models
      ? [
          {
            key: "model",
            group: session,
            icon: <RiBox3Line />,
            label: t("wit.cmd.model"),
            value: chosen,
            options: [
              ...(listedDefault
                ? []
                : [
                    {
                      key: "model:",
                      group: session,
                      label: t("wit.model.default", model),
                      checked: !models.ref,
                      run: now(() => models.pick("")),
                    },
                  ]),
              ...(choices ?? []).map((c) => ({
                key: `model:${c.ref}`,
                group: session,
                label: c.name,
                hint: c.service,
                checked: (models.ref || listedDefault?.ref) === c.ref,
                // picking the default follows it again (no model of its own)
                run: now(() => models.pick(c.ref === listedDefault?.ref ? "" : c.ref)),
              })),
              ...(onModels
                ? [
                    {
                      key: "model:settings",
                      group: session,
                      icon: <RiSettings3Line />,
                      label: t("wit.model"),
                      run: now(onModels),
                    },
                  ]
                : []),
            ],
          },
        ]
      : []),
  ];
  const conversation = (
    <MessageScrollerProvider autoScroll>
      <MessageScroller className="min-h-0 flex-1">
        <MessageScrollerViewport>
          <MessageScrollerContent
            // pb-6 clears the input's fade (h-6) and no more: the last line ends just above it.
            // Step lines (trace's text-sm) take the text's size: only their grey sets them apart.
            className={cn(
              "[&_[data-slot=trace-line]]:text-md",
              page ? "mx-auto w-full max-w-202 gap-5 p-6" : "gap-2.5 p-4 pb-6",
            )}
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
                      <div className="flex items-center gap-1.5">
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
                        {m.metadata?.time && <MessageTime ms={m.metadata.time} />}
                      </div>
                    </MessageContent>
                  </Message>
                ) : (
                  <Answer
                    m={m}
                    live={running && m === last}
                    nums={nums}
                    approvals={approvals}
                    review={review}
                    decisions={decisions}
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
            {/* a compaction, as a step of the conversation: under way, then done */}
            {(compacting || compacted) && (
              <TraceLine status={compacting ? "running" : undefined} role="status">
                <TraceIcon>
                  <RiContractUpDownLine />
                </TraceIcon>
                <TraceTitle>
                  {t(compacting ? "wit.usage.compacting" : "wit.usage.compacted")}
                </TraceTitle>
              </TraceLine>
            )}
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
      {slash !== undefined && commands.length > 0 && !locked && (
        <CommandPicker
          commands={commands}
          query={slash}
          onClose={() => setSlash(undefined)}
          keys={commandKeys}
          t={t}
        />
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
          if (slash !== undefined && commands.length > 0 && commandKeys.current?.key(e)) return;
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
                  // the model chip's own padding is the space between them: the ring sits as far
                  // from its name as its chevron from the send button
                  className={cn("ml-auto", model && "-mr-2")}
                  aria-label={t("wit.usage")}
                />
              }
            >
              <UsageRing value={usage.history} />
            </PopoverTrigger>
            <PopoverContent side="top" className="flex w-72 flex-col gap-3 text-sm">
              <ContextUsage usage={usage} t={t} />
              {transport.compacts && (
                <div className="flex items-center justify-between gap-3 border-t-[0.5px] pt-3">
                  <span className="text-muted-foreground">
                    {t("wit.ctx.until", Math.max(0, 100 - Math.round(usage.history * 100)))}
                  </span>
                  <Button
                    variant="secondary"
                    size="sm"
                    className="shrink-0 font-normal"
                    disabled={compacting || running || !usage.compactable}
                    onClick={compact}
                  >
                    {compacting && <RiLoaderLine className="size-3.5 animate-spin" />}
                    {t(compacting ? "wit.usage.compacting" : "wit.ctx.compact")}
                  </Button>
                </div>
              )}
              {(usage.context > 0 || usage.input > 0) && (
                <div className="flex flex-col gap-0.5 text-muted-foreground text-xs tabular-nums">
                  {usage.context > 0 && (
                    <span>{t("wit.usage.last", usage.context.toLocaleString())}</span>
                  )}
                  {usage.input > 0 && (
                    <span>
                      {t(
                        "wit.usage.total",
                        usage.input.toLocaleString(),
                        usage.output.toLocaleString(),
                      )}
                    </span>
                  )}
                </div>
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
        {running ? (
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

  // the conversation fades out right above the input, which is solid to the card's bottom edge
  const fade = (
    <div
      aria-hidden
      className="pointer-events-none absolute inset-x-0 bottom-full h-6 bg-linear-to-b from-transparent to-background"
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
            <div className="mx-auto flex w-full max-w-202 flex-col gap-2 px-6 pb-6">
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
