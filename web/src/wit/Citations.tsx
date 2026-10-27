import { cn } from "cn";
import type { Citation } from "@/mygo";
import { currentLocale, type T } from "@/shared/i18n";

/**
 * Citations' numbers through the conversation, by message ID; suspect once any citation is, with
 * the first quote of the suspect text (mail text: plain text only).
 */
export type Nums = Map<number, { n: number; suspect: boolean; quote: string; c: Citation }>;

/** A citation's date, absolute: localized for current year (e.g. 01.10. or 10/01), full date for another year ("" for none). */
export function citeDate(ms: number, locale = currentLocale()): string {
  if (!ms) return "";
  const d = new Date(ms);
  const sameYear = d.getFullYear() === new Date().getFullYear();
  return new Intl.DateTimeFormat(locale, sameYear
    ? { month: "numeric", day: "2-digit" }
    : { year: "numeric", month: "numeric", day: "2-digit" }
  ).format(d);
}

/**
 * The mail an answer cites (引用的邮件), unfolded under it: each its number, sender, date and
 * subject, two to a row when there is room; a suspect one noted and edged in the warning colour.
 * A click opens it.
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
    <section
      aria-label={t("ask.citations")}
      className={cn("grid grid-cols-1 gap-1.5 @xl:grid-cols-2", className)}
    >
      {items.map((c) => {
        const suspect = nums.get(c.messageId)?.suspect ?? c.suspect;
        return (
          <button
            key={c.messageId}
            type="button"
            // a message deleted since: listed, with nothing to open
            disabled={c.gone}
            onClick={() => onOpen(c.messageId)}
            className={cn(
              "flex min-w-0 gap-2 rounded-lg border-[0.5px] bg-background px-2.5 py-2 text-left enabled:hover:bg-accent disabled:opacity-60",
              suspect && "border-(--warning-border)",
            )}
          >
            <span className="flex size-4.5 shrink-0 items-center justify-center rounded-sm bg-muted text-muted-foreground text-xs tabular-nums">
              {nums.get(c.messageId)?.n ?? c.n}
            </span>
            <span className="flex min-w-0 flex-1 flex-col">
              <span className="flex items-baseline gap-2 text-muted-foreground text-xs">
                <span className="min-w-0 flex-1 truncate">{c.from}</span>
                <span className="shrink-0">{citeDate(c.date)}</span>
              </span>
              <span className="truncate text-sm">{c.subject || t("mail.no_subject")}</span>
              {suspect && (
                <span className="text-(--warning-text) text-xs">{t("ask.citation.suspect")}</span>
              )}
            </span>
          </button>
        );
      })}
    </section>
  );
}
