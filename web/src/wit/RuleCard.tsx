import { RiShieldLine } from "@remixicon/react";
import { useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { Approval } from "@/mygo";
import type { T } from "@/shared/i18n";

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

/** The rule as the rules page says it (app/settings_rules.go summary): when, if, then. */
function ruleText(p: Proposal, t: T) {
  const r = p.rule;
  const label = (slug = "") => p.labels?.[slug] ?? slug;
  const when =
    r.trigger === "label"
      ? t("settings.rules.trigger.label.summary", label(r.trigger_arg))
      : r.trigger === "security"
        ? t(
            "settings.rules.trigger.security.summary",
            t(`settings.rules.security.${r.trigger_arg}`),
          )
        : t(`settings.rules.trigger.${r.trigger}`);
  const cond = (c: Cond) => {
    if (c.field === "attachments") return t(`settings.rules.attachments.${c.value}`);
    if (c.field === "age") return t("settings.rules.cond.age", Number(c.value));
    const v =
      c.field === "label"
        ? label(c.value)
        : c.field === "folder" && ROLES.includes(c.value)
          ? t(`mail.folder.${c.value}`)
          : c.value;
    return t(
      "settings.rules.cond",
      t(`settings.rules.field.${c.field}`),
      t(`settings.rules.op.${c.op}`),
      v,
    );
  };
  const ifs = (r.conditions ?? []).reduce(
    (acc, c, i) => (i === 0 ? cond(c) : t(`settings.rules.join.${r.match}`, acc, cond(c))),
    "",
  );
  const act = (a: Act) =>
    a.kind === "label" || a.kind === "unlabel"
      ? t(`settings.rules.action.${a.kind}.summary`, label(a.label))
      : a.kind === "forward" && a.to
        ? t("settings.rules.action.forward.summary", a.to)
        : FILE_KINDS.includes(a.kind) && a.dir
          ? t(`settings.rules.action.${a.kind}.summary`, a.dir)
          : t(`settings.rules.action.${a.kind}`);
  return { when, ifs, thens: (r.actions ?? []).map(act) };
}

/**
 * Wit's rule proposal waiting for the user (ADR 0030): the rule as 名称 / 当 / 如果 / 就 / 运行方式, its
 * forward recipients (first-time ones flagged), the model host its Wit actions send mail to and the
 * existing mail it matches; 拒绝, 在编辑器里修改
 * and 保存规则. A rule that forwards saves only through the recipient confirmation here.
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
  const p = proposalOf(a);
  if (!p) return null;
  const { when, ifs, thens } = ruleText(p, t);
  // Forward recipients are addresses; any other destination is the model host a Wit action sends mail to.
  const rcpts = a.destinations.filter((d) => d.includes("@"));
  const hosts = a.destinations.filter((d) => !d.includes("@"));
  const first = (to: string) =>
    (p.first_time ?? []).some((f) => f.toLowerCase() === to.toLowerCase());
  const row = (k: string, v: React.ReactNode) => (
    <div className="flex items-start gap-3.5">
      <span className="w-16 shrink-0 text-muted-foreground">{k}</span>
      <span className="min-w-0 break-words">{v}</span>
    </div>
  );
  const firstBadge = (to?: string) =>
    to &&
    first(to) && (
      <Badge
        variant="outline"
        className="border-(--warning-border) bg-(--warning-bg) text-(--warning-text)"
      >
        {t("mcp_send.first")}
      </Badge>
    );
  const recipient = (to: string) => (
    <span key={to} className="flex items-center gap-1.5">
      <span className="font-medium">{to}</span>
      {firstBadge(to)}
    </span>
  );
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
      {confirm ? (
        <div className="flex flex-col gap-2 px-3.5 py-3 text-md">
          <p className="font-semibold">{t("settings.rules.confirm.title")}</p>
          <p className="text-muted-foreground">{t("settings.rules.confirm.desc", p.rule.name)}</p>
          {rcpts.map(recipient)}
          <p className="text-muted-foreground text-sm">{t("settings.rules.confirm.outbox")}</p>
          <p className="text-muted-foreground text-sm">{t("settings.rules.confirm.again")}</p>
        </div>
      ) : (
        <div className="flex flex-col gap-1.5 px-3.5 py-3 text-md">
          {row(t("settings.rules.name"), p.rule.name || t("settings.rules.untitled"))}
          {row(t("settings.rules.when"), when)}
          {ifs && row(t("settings.rules.if"), ifs)}
          {row(
            t("settings.rules.then"),
            <span className="flex flex-col gap-0.5">
              {thens.map((s, i) => {
                const ac = p.rule.actions?.[i];
                return (
                  // biome-ignore lint/suspicious/noArrayIndexKey: actions are in order and may repeat
                  <span key={i} className="flex items-center gap-1.5">
                    {s}
                    {ac?.kind === "forward" && firstBadge(ac.to)}
                  </span>
                );
              })}
            </span>,
          )}
          {hosts.length > 0 && row(t("wit.approval.sends_to"), hosts.join(", "))}
          {row(
            t("settings.rules.run"),
            t(p.background ? "settings.rules.run.background" : "settings.rules.run.window"),
          )}
          {row(
            t("settings.rules.preview"),
            <span className="flex flex-col gap-0.5">
              <span>
                {p.matches > 0
                  ? t("wit.rule.matches", p.matches)
                  : t("settings.rules.preview.none")}
              </span>
              {(p.mail ?? []).map((m) => (
                <span key={m.id} className="truncate text-muted-foreground text-sm">
                  {m.subject || t("mail.no_subject")} · {m.from}
                </span>
              ))}
              <span className="text-muted-foreground text-sm">{t("wit.rule.new_only")}</span>
            </span>,
          )}
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2 border-muted border-t-[0.5px] px-3.5 py-2.5">
        {!confirm && (
          <span className="flex items-center gap-1.5 text-muted-foreground text-sm">
            <RiShieldLine className="size-3.5" />
            {t("wit.rule.hint")}
          </span>
        )}
        <span className="ml-auto flex flex-wrap gap-2">
          {confirm ? (
            <>
              <Button
                variant="outline"
                className="px-3 font-normal text-md"
                disabled={busy}
                onClick={() => setConfirm(false)}
              >
                {t("settings.rules.confirm.back")}
              </Button>
              <Button
                className="px-3.5 text-md"
                disabled={busy}
                onClick={() => decide(true, JSON.stringify({ confirmed: rcpts }))}
              >
                {t("wit.rule.confirm_save")}
              </Button>
            </>
          ) : (
            <>
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
                {t("settings.rules.save_confirm")}
              </Button>
            </>
          )}
        </span>
      </div>
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
