import { RiChatQuoteLine, RiExternalLinkLine } from "@remixicon/react";
import { cn } from "cn";
import { useState } from "react";
import { Button } from "@/components/button";
import type { T } from "@/shared/i18n";
import { imageURL } from "@/shared/imageURL";
import { citeDate } from "./Citations";

/** The sender's name as the reader shows it: the display name, else the address. */
export function senderName(from: string): string {
  const m = /^\s*"?(.*?)"?\s*<([^>]*)>\s*$/.exec(from);
  if (!m) return from.trim();
  return m[1].trim() || m[2].trim();
}

// The label colours but gray (internal/theme LabelNames[1:]).
const TONES = [
  "red",
  "orange",
  "amber",
  "yellow",
  "lime",
  "green",
  "emerald",
  "teal",
  "cyan",
  "sky",
  "blue",
  "indigo",
  "violet",
  "fuchsia",
  "pink",
];

/** A sender's avatar label colour: fnv-1a of the name, as internal/kit Avatar picks it, so each keeps theirs. */
export function avatarTone(name: string): string {
  let h = 0x811c9dc5;
  for (const b of new TextEncoder().encode(name)) h = Math.imul(h ^ b, 0x01000193) >>> 0;
  return TONES[h % TONES.length];
}

const avatarStyle = (name: string) => ({
  background: `var(--label-${avatarTone(name)}-fill)`,
  color: `var(--label-${avatarTone(name)}-text)`,
});

/**
 * A mail as a card: a small avatar on the sender line with the date, the subject and a snippet,
 * then 查看邮件 / 引用邮件 for the actions it is given: along its foot (in a popover), or, as a
 * card in an answer (card), framed, no wider than it needs, the actions taking the date's place
 * on its sender line on hover or focus. With messageId, the sender's avatar (godwit-img://a<id>,
 * under the message's image policy) covers the initial once it loads.
 */
export function MailCard({
  messageId,
  from,
  date,
  subject,
  snippet,
  onOpen,
  onQuote,
  card = false,
  t,
  className,
}: {
  messageId?: number;
  from: string;
  /** Unix milliseconds; 0 for none. */
  date: number;
  subject: string;
  snippet: string;
  onOpen?(): void;
  onQuote?(): void;
  card?: boolean;
  t: T;
  className?: string;
}) {
  const name = senderName(from);
  const [avatar, setAvatar] = useState(false);
  const actions = (onOpen || onQuote) && (
    <div
      role="toolbar"
      aria-label={subject}
      className={cn(
        "flex gap-0.5",
        card
          ? "pointer-events-none absolute top-1/2 right-0 -translate-y-1/2 bg-background opacity-0 transition-opacity group-focus-within:pointer-events-auto group-focus-within:opacity-100 group-hover:pointer-events-auto group-hover:opacity-100"
          : "-mx-1.5 mt-2 border-t-[0.5px] pt-1.5",
      )}
    >
      {onOpen && (
        <Button variant="ghost" size="xs" onClick={onOpen}>
          <RiExternalLinkLine className="text-muted-foreground" />
          {t("wit.mail.open")}
        </Button>
      )}
      {onQuote && (
        <Button variant="ghost" size="xs" onClick={onQuote}>
          <RiChatQuoteLine className="text-muted-foreground" />
          {t("wit.quote")}
        </Button>
      )}
    </div>
  );
  return (
    <div
      className={cn(
        "flex min-w-0 flex-col text-sm",
        card && "group max-w-md rounded-xl border-[0.5px] bg-background px-2.5 py-2",
        className,
      )}
    >
      <div className="flex items-center gap-2">
        <span
          aria-hidden="true"
          style={avatarStyle(name)}
          className="relative flex size-5 shrink-0 items-center justify-center overflow-hidden rounded-lg font-medium text-xs"
        >
          {[...name][0]?.toUpperCase() ?? "?"}
          {messageId !== undefined && (
            <img
              src={imageURL(`a${messageId}`)}
              alt=""
              onLoad={() => setAvatar(true)}
              className={cn(
                "absolute inset-0 size-full bg-background object-cover transition-opacity",
                !avatar && "opacity-0",
              )}
            />
          )}
        </span>
        <span className="min-w-0 flex-1 truncate font-medium">{name}</span>
        {card ? (
          <span className="relative flex shrink-0 items-center">
            <span className="text-muted-foreground text-xs transition-opacity group-focus-within:opacity-0 group-hover:opacity-0">
              {citeDate(date)}
            </span>
            {actions}
          </span>
        ) : (
          <span className="shrink-0 text-muted-foreground text-xs">{citeDate(date)}</span>
        )}
      </div>
      <span className="mt-0.5 truncate text-sm">{subject || t("mail.no_subject")}</span>
      {snippet && <span className="truncate text-muted-foreground text-xs">{snippet}</span>}
      {!card && actions}
    </div>
  );
}
