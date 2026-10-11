import { useChat } from "@ai-sdk/react";
import {
  RiAlertLine,
  RiArrowDownLine,
  RiArrowDownSLine,
  RiArrowUpLine,
  RiLoaderLine,
  RiMagicLine,
  RiStopFill,
  RiTranslate2,
} from "@remixicon/react";
import type { DynamicToolUIPart } from "ai";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { Alert, AlertAction, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Bubble, BubbleContent } from "@/components/ui/bubble";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
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
import type { Approval, Citation, WitHistory, WitInfo } from "@/mygo";
import type { T } from "@/shared/i18n";
import { Citations, type Nums } from "./Citations";
import { Response } from "./Response";
import { historyToMessages, type WitMeta, type WitTransport, type WitUIMessage } from "./transport";

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
      else out[i].suspect ||= c.suspect;
    }
  }
  return out;
}

/** "调用了 n 个工具 · a ×2、b": the distinct tools, each with its count. */
function toolsLabel(calls: DynamicToolUIPart[], t: T): string {
  const count = new Map<string, number>();
  for (const c of calls) count.set(c.toolName, (count.get(c.toolName) ?? 0) + 1);
  const names = [...count].map(([n, k]) => (k > 1 ? `${n} ×${k}` : n));
  const key = count.size === 1 ? "wit.tools.one" : "wit.tools.other";
  return `${t(key, count.size)} · ${names.join(t("mail.list_sep"))}`;
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
  t,
}: {
  a: Approval;
  many: boolean;
  busy: boolean;
  decide(accept: boolean, all: boolean): void;
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
  return (
    <Card size="sm" className="ring-primary">
      <CardHeader>
        <CardTitle>{t("wit.approval.wants", toolLabel(a.tool, t))}</CardTitle>
        <CardDescription className="font-mono text-xs">{a.tool}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-1.5">
        {a.summary && <p className="text-muted-foreground">{a.summary}</p>}
        {fields.map(
          ([label, value]) =>
            value && (
              <div key={label} className="flex items-start gap-3">
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
      </CardContent>
      <CardFooter className="flex-wrap gap-2">
        <Button size="sm" disabled={busy} onClick={() => decide(true, false)}>
          {t("wit.approval.allow")}
        </Button>
        {many && (
          <Button size="sm" variant="outline" disabled={busy} onClick={() => decide(true, true)}>
            {t("wit.approval.allow_all")}
          </Button>
        )}
        <Button size="sm" variant="outline" disabled={busy} onClick={() => decide(false, false)}>
          {t("wit.approval.reject")}
        </Button>
        {a.tool === "create_draft" && (
          <span className="ml-auto text-muted-foreground text-sm">
            {t("wit.approval.send_note")}
          </span>
        )}
      </CardFooter>
    </Card>
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
  t: T;
}) {
  const calls = m.parts.filter((p): p is DynamicToolUIPart => p.type === "dynamic-tool");
  const cites = citesOf(m);
  const text = m.parts
    .flatMap((p) => (p.type === "text" && p.text.trim() ? [p.text] : []))
    .join("\n\n");
  const requested = calls.flatMap((c) => {
    const a = c.state === "approval-requested" ? approvals.get(c.approval.id) : undefined;
    return a ? [a] : [];
  });
  const waiting = requested.filter((a) => a.id === review?.approval);
  return (
    <div data-role="assistant" className="flex flex-col gap-2.5">
      {calls.length > 0 && (
        <Collapsible>
          <CollapsibleTrigger className="flex items-center gap-1 text-muted-foreground text-sm hover:text-foreground">
            {toolsLabel(calls, t)}
            <RiArrowDownSLine className="size-3.5" />
          </CollapsibleTrigger>
          <CollapsibleContent className="mt-1 flex flex-col gap-1 text-muted-foreground">
            {calls.map((c) => (
              <code key={c.toolCallId} className="break-all text-xs">
                {c.toolName} {clip(JSON.stringify(c.input ?? {}))}
              </code>
            ))}
          </CollapsibleContent>
        </Collapsible>
      )}
      {cites
        .filter((c) => nums.get(c.messageId)?.suspect)
        .map((c) => (
          <Alert key={c.messageId} role="status" className="border-0 bg-muted px-3 py-2.5 text-sm">
            <RiAlertLine />
            <AlertDescription>{t("wit.suspect", `[${nums.get(c.messageId)?.n}]`)}</AlertDescription>
          </Alert>
        ))}
      {text ? (
        <Response text={text} streaming={live} t={t} onLink={onLink} />
      ) : (
        live && <Busy t={t} />
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
        <Card key={a.id} size="sm" className="pt-0 ring-primary">
          <CardHeader className="bg-muted py-2">
            <CardTitle>{t("wit.compose.review")}</CardTitle>
            <CardDescription className="font-mono text-xs">{a.tool}</CardDescription>
          </CardHeader>
          <CardContent>{review?.body}</CardContent>
        </Card>
      ))}
      {requested.filter((a) => !isDraftEdit(a.tool)).map(tools)}
    </div>
  );
}

/** The edit the page reviews: its card in the panel holds `body` (the summary and the decisions). */
export type PanelReview = { approval: number; body: ReactNode };

/**
 * Wit's conversation and input, in either host. As the compose window's panel (layout "panel"):
 * what Wit knows (chips), 调整 / 翻译 (`info`), the conversation, and the input. As the Wit page
 * (layout "page"): the conversation centred, the input floating under it with `slots.input` (the
 * model chip), the cited mail opening by onCite in a column when the page is wide, else under each
 * answer. Links open through the host's onLink (after the confirmation); its failure toasts.
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
  slots,
  ask: handed,
  onAsked,
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
  slots?: { chips?: ReactNode; actions?: ReactNode; input?: ReactNode };
  /** A question the host hands over: asked once (by identity), then onAsked. */
  ask?: { text: string };
  onAsked?(): void;
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
  const submit = () => {
    const text = draft.trim();
    if (!text || locked || running) return;
    setDraft("");
    ask(text);
  };
  const fail = (e: unknown) =>
    toast.error(witError(String((e as Error | undefined)?.message ?? e), t));
  const stop = () => void transport.stop().catch(fail);
  const link = (url: string) => void onLink(url).catch(fail);

  // Citations numbered through the conversation: the history's as given, live ones (N=0) after;
  // a message is suspect once any citation of it is.
  const nums: Nums = new Map();
  const approvals = new Map<string, Approval>();
  for (const m of chat.messages) {
    for (const c of citesOf(m)) {
      const k = nums.get(c.messageId);
      if (k) k.suspect ||= c.suspect;
      else {
        const next = Math.max(0, ...[...nums.values()].map((x) => x.n)) + 1;
        nums.set(c.messageId, { n: c.n || next, suspect: c.suspect });
      }
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
        t={t}
      />
    );
  const page = layout === "page";
  const conversation = (
    <MessageScrollerProvider autoScroll>
      <MessageScroller className="min-h-0 flex-1">
        <MessageScrollerViewport>
          <MessageScrollerContent
            className={page ? "mx-auto w-full max-w-190 gap-5 p-6" : "gap-4 px-4 py-2"}
          >
            {chat.messages.map((m) => (
              <MessageScrollerItem key={m.id} messageId={m.id}>
                {m.role === "user" ? (
                  <Message align="end">
                    <MessageContent>
                      <Bubble align="end">
                        <BubbleContent className="whitespace-pre-wrap text-base">
                          {m.parts.flatMap((p) => (p.type === "text" ? [p.text] : [])).join("")}
                        </BubbleContent>
                      </Bubble>
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
                    t={t}
                  />
                )}
              </MessageScrollerItem>
            ))}
            {running && last?.role !== "assistant" && <Busy t={t} />}
          </MessageScrollerContent>
        </MessageScrollerViewport>
        <MessageScrollerButton>
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
  const input = (
    <InputGroup className="relative rounded-2xl border-0 bg-background shadow-(--shadow-float)">
      <InputGroupTextarea
        aria-label={t(page ? "wit.input" : "wit.compose.input")}
        placeholder={t(page ? "wit.placeholder" : "wit.compose.placeholder")}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key !== "Enter" || e.shiftKey || e.nativeEvent.isComposing || e.keyCode === 229)
            return;
          e.preventDefault();
          submit();
        }}
        className={page ? "max-h-44 min-h-14 text-md" : "max-h-40 min-h-16 text-md"}
      />
      <InputGroupAddon align="block-end" className={slots?.input ? undefined : "justify-end"}>
        {slots?.input}
        {slots?.input && <span className="flex-1" />}
        {running ? (
          <InputGroupButton
            variant="outline"
            size="icon-sm"
            aria-label={t("wit.stop")}
            onClick={stop}
          >
            <RiStopFill />
          </InputGroupButton>
        ) : (
          <InputGroupButton
            variant="default"
            size="icon-sm"
            aria-label={t("wit.send")}
            disabled={locked || !draft.trim()}
            onClick={submit}
          >
            <RiArrowUpLine />
          </InputGroupButton>
        )}
      </InputGroupAddon>
    </InputGroup>
  );

  if (page) {
    const cites = citesOf(...chat.messages);
    return (
      <div className="@container flex h-full min-h-0">
        <div className="flex min-w-0 flex-1 flex-col">
          {conversation}
          <div className="mx-auto flex w-full max-w-190 flex-col gap-2 px-6 pb-4">
            {error}
            {input}
          </div>
        </div>
        {cites.length > 0 && onCite && (
          // wide: the column (narrow: under each answer, Answer)
          <aside className="hidden w-80 shrink-0 flex-col overflow-auto border-l bg-sidebar p-3 @4xl:flex">
            <Citations items={cites} nums={nums} onOpen={onCite} t={t} />
          </aside>
        )}
      </div>
    );
  }
  // 调整 / 翻译: a tray the input overlaps from below.
  const trayButton = (Icon: typeof RiMagicLine, label: string) => (
    <Button variant="ghost" size="sm" className="gap-1 text-sm">
      <Icon className="size-3.5 text-muted-foreground" />
      {label}
      <RiArrowDownSLine className="size-3 text-muted-foreground" />
    </Button>
  );
  return (
    <aside className="flex w-80 shrink-0 flex-col border-l">
      {info && (info.chips.length > 0 || slots?.chips) && (
        <div className="flex flex-wrap items-center gap-1.5 px-4 py-3">
          {info.chips.map((c) => (
            <Badge key={c} variant="secondary" className="max-w-full truncate rounded-sm">
              {c}
            </Badge>
          ))}
          {slots?.chips}
        </div>
      )}
      {conversation}
      {error && <div className="px-4 pb-2">{error}</div>}
      <div className="px-4 pb-3">
        {info && (
          <div data-tray className="mx-1 -mb-4 flex gap-1 rounded-t-2xl bg-muted px-1 pt-1 pb-5">
            <DropdownMenu>
              <DropdownMenuTrigger
                disabled={locked}
                render={trayButton(RiMagicLine, t("wit.compose.adjust"))}
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
            {slots?.actions}
          </div>
        )}
        {input}
      </div>
    </aside>
  );
}
