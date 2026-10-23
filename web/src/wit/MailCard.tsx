import { RiChatQuoteLine, RiExternalLinkLine } from "@remixicon/react";
import { cn } from "cn";
import { Button } from "@/components/ui/button";
import type { T } from "@/shared/i18n";
import { citeDate } from "./Citations";

/** The sender's name as the reader shows it: the display name, else the address. */
export function senderName(from: string): string {
  const m = /^\s*"?(.*?)"?\s*<([^>]*)>\s*$/.exec(from);
  if (!m) return from.trim();
  return m[1].trim() || m[2].trim();
}

// The label colours but gray (internal/theme LabelNames[1:]): -100 fill, -700 text; dark -950, -300.
const TONES = [
  "bg-red-100 text-red-700 dark:bg-red-950 dark:text-red-300",
  "bg-orange-100 text-orange-700 dark:bg-orange-950 dark:text-orange-300",
  "bg-amber-100 text-amber-700 dark:bg-amber-950 dark:text-amber-300",
  "bg-yellow-100 text-yellow-700 dark:bg-yellow-950 dark:text-yellow-300",
  "bg-lime-100 text-lime-700 dark:bg-lime-950 dark:text-lime-300",
  "bg-green-100 text-green-700 dark:bg-green-950 dark:text-green-300",
  "bg-teal-100 text-teal-700 dark:bg-teal-950 dark:text-teal-300",
  "bg-sky-100 text-sky-700 dark:bg-sky-950 dark:text-sky-300",
  "bg-blue-100 text-blue-700 dark:bg-blue-950 dark:text-blue-300",
  "bg-violet-100 text-violet-700 dark:bg-violet-950 dark:text-violet-300",
  "bg-pink-100 text-pink-700 dark:bg-pink-950 dark:text-pink-300",
];

/** A sender's avatar colour: fnv-1a of the name, as internal/kit Avatar picks it, so each keeps theirs. */
export function avatarTone(name: string): string {
  let h = 0x811c9dc5;
  for (const b of new TextEncoder().encode(name)) h = Math.imul(h ^ b, 0x01000193) >>> 0;
  return TONES[h % TONES.length];
}

/**
 * A mail as a card: a small avatar on the sender line with the date, the subject and a snippet,
 * then 查看邮件 / 引用邮件 for the actions it is given.
 */
export function MailCard({
  from,
  date,
  subject,
  snippet,
  onOpen,
  onQuote,
  t,
  className,
}: {
  from: string;
  /** Unix milliseconds; 0 for none. */
  date: number;
  subject: string;
  snippet: string;
  onOpen?(): void;
  onQuote?(): void;
  t: T;
  className?: string;
}) {
  const name = senderName(from);
  return (
    <div className={cn("flex min-w-0 flex-col text-sm", className)}>
      <div className="flex items-center gap-2">
        <span
          aria-hidden="true"
          className={cn(
            "flex size-5 shrink-0 items-center justify-center rounded-[5px] font-semibold text-xs",
            avatarTone(name),
          )}
        >
          {[...name][0]?.toUpperCase() ?? "?"}
        </span>
        <span className="min-w-0 flex-1 truncate font-medium">{name}</span>
        <span className="shrink-0 text-muted-foreground text-xs">{citeDate(date)}</span>
      </div>
      <span className="mt-0.5 truncate">{subject || t("mail.no_subject")}</span>
      {snippet && <span className="truncate text-muted-foreground text-xs">{snippet}</span>}
      {(onOpen || onQuote) && (
        <div
          role="toolbar"
          aria-label={subject}
          className="-mx-1.5 mt-2 flex gap-0.5 border-t-[0.5px] pt-1.5"
        >
          {onOpen && (
            <Button variant="ghost" size="xs" className="font-normal" onClick={onOpen}>
              <RiExternalLinkLine className="text-muted-foreground" />
              {t("wit.mail.open")}
            </Button>
          )}
          {onQuote && (
            <Button variant="ghost" size="xs" className="font-normal" onClick={onQuote}>
              <RiChatQuoteLine className="text-muted-foreground" />
              {t("wit.quote")}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
