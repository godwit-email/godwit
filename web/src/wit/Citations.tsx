import { cn } from "cn";
import type { Citation } from "@/mygo";
import type { T } from "@/shared/i18n";

/** Citations' numbers through the conversation, by message ID; suspect once any citation is. */
export type Nums = Map<number, { n: number; suspect: boolean }>;

/** A citation's date, absolute: M/DD this year, YYYY/M/DD another ("" for none). */
export function citeDate(ms: number): string {
  if (!ms) return "";
  const d = new Date(ms);
  const md = `${d.getMonth() + 1}/${String(d.getDate()).padStart(2, "0")}`;
  return d.getFullYear() === new Date().getFullYear() ? md : `${d.getFullYear()}/${md}`;
}

/**
 * The mail an answer cites (引用的邮件): each 「[n] 发件人」, its date and subject, a suspect one
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
  return (
    <section aria-label={t("ask.citations")} className={cn("flex flex-col gap-2.5", className)}>
      <h2 className="font-semibold text-md">{t("ask.citations")}</h2>
      {items.map((c) => {
        const suspect = nums.get(c.messageId)?.suspect ?? c.suspect;
        return (
          <button
            key={c.messageId}
            type="button"
            onClick={() => onOpen(c.messageId)}
            className={cn(
              "flex flex-col gap-1 rounded-xl border bg-background px-3 py-2.5 text-left hover:bg-accent",
              suspect && "border-(--warning-border)",
            )}
          >
            <span className="flex items-baseline gap-2 leading-tight">
              <span className="min-w-0 flex-1 truncate text-muted-foreground text-sm">
                [{nums.get(c.messageId)?.n ?? c.n}] {c.from}
              </span>
              <span className="shrink-0 text-muted-foreground text-sm">{citeDate(c.date)}</span>
            </span>
            <span className="truncate text-foreground text-md leading-tight">
              {c.subject || t("mail.no_subject")}
            </span>
            {suspect && (
              <span className="text-(--warning-text) text-sm leading-tight">
                {t("ask.citation.suspect")}
              </span>
            )}
          </button>
        );
      })}
    </section>
  );
}
