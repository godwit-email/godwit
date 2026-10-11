import { cn } from "cn";
import type { Citation } from "@/mygo";
import type { T } from "@/shared/i18n";

/** Citations' numbers through the conversation, by message ID; suspect once any citation is. */
export type Nums = Map<number, { n: number; suspect: boolean }>;

/** A list time, as the native mail list's (app listTime): today's clock, 昨天, this week's day, else M/D. */
export function listTime(now: Date, ms: number, t: T): string {
  if (!ms) return "";
  const d = new Date(ms);
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const day = 86_400_000;
  if (d >= today)
    return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
  const yesterday = new Date(today.getFullYear(), today.getMonth(), today.getDate() - 1);
  if (d >= yesterday) return t("mail.yesterday");
  const monday = new Date(today.getTime() - ((now.getDay() + 6) % 7) * day);
  if (d >= monday) return t(`mail.weekday.${d.getDay()}`);
  return `${d.getMonth() + 1}/${d.getDate()}`;
}

/**
 * The mail an answer cites (引用的邮件): each 「[n] 发件人」, its time and subject, a suspect one
 * noted and edged in the warning colour; a click opens it. The Wit page shows it as a column when
 * wide and under each answer when narrow (a container query on the page, Panel).
 */
export function Citations({
  items,
  nums,
  onOpen,
  t,
  className,
}: {
  items: Citation[];
  nums: Nums;
  onOpen(id: number): void;
  t: T;
  className?: string;
}) {
  const now = new Date();
  return (
    <section aria-label={t("ask.citations")} className={cn("flex flex-col gap-2", className)}>
      <h2 className="px-1 font-semibold text-sm">{t("ask.citations")}</h2>
      {items.map((c) => {
        const suspect = nums.get(c.messageId)?.suspect ?? c.suspect;
        return (
          <button
            key={c.messageId}
            type="button"
            onClick={() => onOpen(c.messageId)}
            className={cn(
              "flex flex-col gap-0.5 rounded-lg border bg-background p-2.5 text-left hover:bg-accent",
              suspect && "border-(--warning-border)",
            )}
          >
            <span className="flex items-baseline gap-2">
              <span className="min-w-0 flex-1 truncate font-medium">
                [{nums.get(c.messageId)?.n ?? c.n}] {c.from}
              </span>
              <span className="shrink-0 text-muted-foreground text-xs">
                {listTime(now, c.date, t)}
              </span>
            </span>
            <span className="truncate text-muted-foreground text-sm">
              {c.subject || t("mail.no_subject")}
            </span>
            {suspect && (
              <span className="text-(--warning-text) text-xs">{t("ask.citation.suspect")}</span>
            )}
          </button>
        );
      })}
    </section>
  );
}
