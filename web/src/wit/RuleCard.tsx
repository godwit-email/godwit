import {
  type RemixiconComponentType,
  RiArchiveLine,
  RiArrowRightSLine,
  RiAttachment2,
  RiDeleteBinLine,
  RiDraftLine,
  RiFilePdf2Line,
  RiFileTextLine,
  RiFlashlightLine,
  RiMailDownloadLine,
  RiMailOpenLine,
  RiPriceTag3Line,
  RiShareForwardLine,
  RiShieldCheckLine,
  RiSpam2Line,
  RiStarLine,
  RiStopCircleLine,
} from "@remixicon/react";
import { cn } from "cn";
import { useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { Approval } from "@/mygo";
import type { T } from "@/shared/i18n";
import { citeDate } from "./Citations";

export const PROPOSE_RULE_TOOL = "propose_rule";

/** What core tells the model when the user took the proposal to the editor (core msgToEditor). */
const MSG_TO_EDITOR =
  "the user opened the rule in the editor; it is saved only if they save it there";

type Cond = { field: string; op: string; value: string };
type Act = { kind: string; label?: string; to?: string; dir?: string };

/** propose_rule's recorded arguments (core ruleProposal). */
type Proposal = {
  rule: {
    name: string;
    trigger: string;
    trigger_arg?: string;
    match: string;
    conditions: Cond[] | null;
    actions: Act[] | null;
  };
  background: boolean;
  first_time: string[] | null;
  matches: number;
  mail: { id: number; from: string; subject: string; date: string }[] | null;
  labels: Record<string, string> | null;
  /** The account the rule is for; absent for every account. */
  account_name?: string;
};

const ROLES = ["inbox", "archive", "junk", "trash"];
const FILE_KINDS = ["pdf", "eml", "attachments", "key_points"];

export function proposalOf(a: Approval): Proposal | undefined {
  try {
    const p = JSON.parse(a.args || "{}");
    return p?.rule ? p : undefined;
  } catch {
    return undefined;
  }
}

// Stands in for a value while its sentence is put together, so the value can be set apart (mono).
const MARK = "\u0001";

/** A phrase with its value set apart: the text before it, the value, the text after it. */
type Phrase = { pre: string; val?: string; post?: string };
const phrase = (s: string, val?: string): Phrase => {
  if (val === undefined) return { pre: s };
  const [pre, post = ""] = s.split(MARK);
  return { pre, val, post };
};

/** The rule as the rules page says it (app/settings_rules.go summary): when, if, then. */
function ruleText(p: Proposal, t: T) {
  const r = p.rule;
  const label = (slug = "") => p.labels?.[slug] ?? slug;
  const trigger =
    r.trigger === "label"
      ? t("settings.rules.trigger.label.summary", label(r.trigger_arg))
      : r.trigger === "security"
        ? t(
            "settings.rules.trigger.security.summary",
            t(`settings.rules.security.${r.trigger_arg}`),
          )
        : t(`settings.rules.trigger.${r.trigger}`);
  const when = p.account_name
    ? t("wit.rule.when_account", trigger, p.account_name)
    : `${trigger} · ${t("settings.rules.account_all")}`;
  const cond = (c: Cond): Phrase => {
    if (c.field === "attachments") return phrase(t(`settings.rules.attachments.${c.value}`));
    if (c.field === "age") return phrase(t("settings.rules.cond.age", Number(c.value)));
    const v =
      c.field === "label"
        ? label(c.value)
        : c.field === "folder" && ROLES.includes(c.value)
          ? t(`mail.folder.${c.value}`)
          : c.value;
    const s = t(
      "settings.rules.cond",
      t(`settings.rules.field.${c.field}`),
      t(`settings.rules.op.${c.op}`),
      MARK,
    );
    return phrase(s, v);
  };
  const act = (a: Act): Phrase =>
    a.kind === "label" || a.kind === "unlabel"
      ? phrase(t(`settings.rules.action.${a.kind}.summary`, label(a.label)))
      : a.kind === "forward" && a.to
        ? phrase(t("settings.rules.action.forward.summary", MARK), a.to)
        : FILE_KINDS.includes(a.kind) && a.dir
          ? phrase(t(`settings.rules.action.${a.kind}.summary`, a.dir))
          : phrase(t(`settings.rules.action.${a.kind}`));
  // 且 / 或 between the conditions, as the join puts it
  const join = t(`settings.rules.join.${r.match}`, "", "").trim();
  return { when, ifs: (r.conditions ?? []).map(cond), join, thens: (r.actions ?? []).map(act) };
}

const ACTION_ICONS: Record<string, RemixiconComponentType> = {
  label: RiPriceTag3Line,
  unlabel: RiPriceTag3Line,
  read: RiMailOpenLine,
  star: RiStarLine,
  archive: RiArchiveLine,
  junk: RiSpam2Line,
  trash: RiDeleteBinLine,
  block: RiSpam2Line,
  stop: RiStopCircleLine,
  forward: RiShareForwardLine,
  pdf: RiFilePdf2Line,
  eml: RiMailDownloadLine,
  attachments: RiAttachment2,
  draft_reply: RiDraftLine,
  key_points: RiFileTextLine,
};

const mono = (ph: Phrase) => (
  <>
    {ph.pre}
    {ph.val !== undefined && <span className="font-mono text-sm">{ph.val}</span>}
    {ph.post}
  </>
);

/**
 * Wit's rule proposal waiting for the user (ADR 0030): the rule as 名称 / 当 / 如果 / 就 / 运行方式, its
 * forward recipients (first-time ones flagged), the model host its Wit actions send mail to and the
 * existing mail it matches; 拒绝, 在编辑器里修改 and 保存规则. A rule that forwards saves only through
 * the recipient confirmation, which takes the footer's place.
 */
export function RuleCard({
  a,
  busy,
  decide,
  onEdit,
  t,
}: {
  a: Approval;
  busy: boolean;
  decide(accept: boolean, editedArgs?: string): void;
  onEdit?(): void;
  t: T;
}) {
  const [confirm, setConfirm] = useState(false);
  const [open, setOpen] = useState(false);
  const p = proposalOf(a);
  if (!p) return null;
  const { when, ifs, join, thens } = ruleText(p, t);
  // Forward recipients are addresses; any other destination is the model host a Wit action sends mail to.
  const rcpts = a.destinations.filter((d) => d.includes("@"));
  const hosts = a.destinations.filter((d) => !d.includes("@"));
  const first = (to: string) =>
    (p.first_time ?? []).some((f) => f.toLowerCase() === to.toLowerCase());
  const k = (s: string) => <span className="whitespace-nowrap text-muted-foreground">{s}</span>;
  const firstBadge = (to?: string) =>
    to &&
    first(to) && (
      <Badge
        variant="outline"
        className="ml-auto shrink-0 border-(--warning-border) bg-(--warning-bg) text-(--warning-text)"
      >
        {t("settings.rules.confirm.first")}
      </Badge>
    );
  const mail = p.mail ?? [];
  return (
    <div className="overflow-hidden rounded-xl border-[0.5px] bg-background shadow-(--shadow-xs)">
      <div className="flex flex-wrap items-center gap-2 border-b-[0.5px] bg-sidebar px-3.5 py-2.5 leading-5">
        <span className="font-semibold">{t("wit.approval.wants", t("wit.tool.propose_rule"))}</span>
        <span className="font-mono text-muted-foreground text-sm">{a.tool}</span>
        <span className="ml-auto flex gap-1.5">
          {a.destinations.length > 0 && (
            <Badge variant="secondary">{t("settings.rules.egress")}</Badge>
          )}
          <Badge
            variant="outline"
            className="border-(--warning-border) bg-(--warning-bg) text-(--warning-text)"
          >
            {t("wit.approval.pending")}
          </Badge>
        </span>
      </div>
      <div className="grid grid-cols-[minmax(4.5rem,auto)_minmax(0,1fr)] gap-x-3 gap-y-2 px-3.5 py-3 text-md">
        {k(t("settings.rules.name"))}
        <span className="font-medium">{p.rule.name || t("settings.rules.untitled")}</span>
        {k(t("settings.rules.when"))}
        <span>{when}</span>
        {ifs.length > 0 && (
          <>
            {k(t("settings.rules.if"))}
            <span className="flex flex-wrap items-center gap-1.5">
              {ifs.map((c, i) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: conditions are in order and may repeat
                <span key={i} className="contents">
                  {i > 0 && <span className="text-muted-foreground text-sm">{join}</span>}
                  <span className="rounded-md bg-muted px-2 leading-6">{mono(c)}</span>
                </span>
              ))}
            </span>
          </>
        )}
        {k(t("settings.rules.then"))}
        <span className="flex flex-col gap-1.5">
          {thens.map((s, i) => {
            const ac = p.rule.actions?.[i];
            const fwd = ac?.kind === "forward";
            const Icon = ACTION_ICONS[ac?.kind ?? ""] ?? RiFlashlightLine;
            return (
              <span
                // biome-ignore lint/suspicious/noArrayIndexKey: actions are in order and may repeat
                key={i}
                className={cn(
                  "flex items-center gap-2 rounded-lg border-[0.5px] px-2.5 py-1.5",
                  fwd && "border-(--warning-border) bg-(--warning-bg)",
                )}
              >
                <Icon
                  className={cn(
                    "size-3.5 shrink-0",
                    fwd ? "text-(--warning-text)" : "text-muted-foreground",
                  )}
                />
                <span className="min-w-0 break-words">
                  {i > 0 ? t("wit.rule.then_next", "") : ""}
                  {mono(s)}
                </span>
                {fwd && firstBadge(ac.to)}
              </span>
            );
          })}
        </span>
        {hosts.length > 0 && (
          <>
            {k(t("wit.approval.sends_to"))}
            <span className="break-words">{hosts.join(", ")}</span>
          </>
        )}
        {k(t("settings.rules.run"))}
        <span>
          {t(p.background ? "settings.rules.run.background" : "settings.rules.run.window")}
        </span>
        {k(t("wit.rule.existing"))}
        <span className="flex flex-col gap-1.5">
          {p.matches > 0 ? (
            <Button
              variant="ghost"
              size="sm"
              className="-ml-1.5 h-6 self-start px-1.5 font-normal text-md"
              aria-expanded={open}
              disabled={mail.length === 0}
              onClick={() => setOpen(!open)}
            >
              {t("wit.rule.matches", p.matches)}
              {mail.length > 0 && (
                <RiArrowRightSLine className={cn("transition-transform", open && "rotate-90")} />
              )}
            </Button>
          ) : (
            <span>{t("settings.rules.preview.none")}</span>
          )}
          {open && (
            <span className="flex flex-col divide-y-[0.5px] overflow-hidden rounded-lg border-[0.5px]">
              {mail.map((m) => (
                <span key={m.id} className="flex gap-2 px-2.5 py-1.5 text-sm">
                  <span className="shrink-0 font-medium">
                    {m.from.replace(/\s*<[^>]*>$/, "") || m.from}
                  </span>
                  <span className="min-w-0 flex-1 truncate">
                    {m.subject || t("mail.no_subject")}
                  </span>
                  <span className="shrink-0 text-muted-foreground">
                    {citeDate(Date.parse(m.date) || 0)}
                  </span>
                </span>
              ))}
            </span>
          )}
          <span className="text-muted-foreground text-sm">
            {t(rcpts.length > 0 ? "wit.rule.new_only_forward" : "wit.rule.new_only")}
          </span>
        </span>
      </div>
      {confirm ? (
        <fieldset
          aria-label={t("settings.rules.confirm.title")}
          className="flex min-w-0 flex-col gap-2.5 border-(--warning-border) border-t-[0.5px] bg-(--warning-bg) px-3.5 py-3 text-md"
        >
          <span className="flex flex-col gap-0.5 text-(--warning-text)">
            <span className="font-semibold">{t("settings.rules.confirm.title")}</span>
            <span className="text-sm">{t("wit.rule.confirm_desc")}</span>
          </span>
          {rcpts.map((to) => (
            <span key={to} className="flex flex-wrap items-center gap-2">
              <span className="font-medium font-mono text-base">{to}</span>
              {firstBadge(to)}
            </span>
          ))}
          <span className="flex flex-col gap-0.5 text-sm">
            <span>
              ·{" "}
              {p.account_name
                ? t("settings.rules.confirm.outbox.from", p.account_name)
                : t("settings.rules.confirm.outbox")}
            </span>
            <span>· {t("wit.rule.confirm_again")}</span>
          </span>
          <span className="flex justify-end gap-2">
            <Button
              variant="outline"
              className="px-3 font-normal text-md"
              disabled={busy}
              onClick={() => setConfirm(false)}
            >
              {t("close.unsaved.back")}
            </Button>
            <Button
              className="px-3.5 text-md"
              disabled={busy}
              onClick={() => decide(true, JSON.stringify({ confirmed: rcpts }))}
            >
              {t("wit.rule.confirm_save")}
            </Button>
          </span>
        </fieldset>
      ) : (
        <div className="flex flex-wrap items-center gap-2 border-muted border-t-[0.5px] px-3.5 py-2.5">
          <span className="flex items-center gap-1.5 text-muted-foreground text-sm">
            <RiShieldCheckLine className="size-3.5" />
            {t("wit.rule.hint")}
          </span>
          <span className="ml-auto flex flex-wrap gap-2">
            <Button
              variant="outline"
              className="px-3 font-normal text-md"
              disabled={busy}
              onClick={() => decide(false)}
            >
              {t("wit.approval.reject")}
            </Button>
            {onEdit && (
              <Button
                variant="outline"
                className="px-3 font-normal text-md"
                disabled={busy}
                onClick={onEdit}
              >
                {t("wit.rule.edit")}
              </Button>
            )}
            <Button
              className="px-3.5 text-md"
              disabled={busy}
              onClick={() => (rcpts.length > 0 ? setConfirm(true) : decide(true))}
            >
              {t("wit.rule.save")}
            </Button>
          </span>
        </div>
      )}
    </div>
  );
}

/** A decided proposal's line: saved and on, opened in the editor, or rejected with nothing made. */
export function ruleSettled(a: Approval, t: T): string {
  if (a.state === "done") return t("wit.rule.saved", proposalOf(a)?.rule.name ?? "");
  if (a.state === "rejected")
    return t(a.error === MSG_TO_EDITOR ? "wit.rule.edited" : "wit.rule.rejected");
  return "";
}
