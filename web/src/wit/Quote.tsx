import { RiCloseLine, RiMailLine } from "@remixicon/react";
import { type KeyboardEvent, useEffect, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import type { MailHit } from "@/mygo";
import type { T } from "@/shared/i18n";

const subjectOf = (h: MailHit, t: T) => h.subject || t("mail.no_subject");

/**
 * The quote picker (引用邮件): a search of the user's mail over the input, each hit a pick.
 * Invented UI (the mockup shows only its entry point), built from the menu's look. Escape closes.
 */
export function QuotePicker({
  search,
  onPick,
  onClose,
  t,
}: {
  search(q: string): Promise<MailHit[]>;
  onPick(h: MailHit): void;
  onClose(): void;
  t: T;
}) {
  const [q, setQ] = useState("");
  const [hits, setHits] = useState<MailHit[]>();
  useEffect(() => {
    if (!q.trim()) {
      setHits(undefined);
      return;
    }
    let live = true;
    const timer = setTimeout(
      () =>
        search(q).then(
          (h) => live && setHits(h),
          () => live && setHits([]),
        ),
      150,
    );
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [q, search]);
  const keys = (e: KeyboardEvent) => {
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      onClose();
    } else if (e.key === "Enter" && !e.nativeEvent.isComposing && hits?.[0]) {
      e.preventDefault();
      onPick(hits[0]);
    }
  };
  return (
    <div
      role="dialog"
      aria-label={t("wit.quote")}
      className="absolute bottom-full left-0 z-3 mb-2 flex w-80 max-w-full flex-col gap-1 rounded-lg border bg-popover p-1 text-popover-foreground shadow-md"
    >
      <Input
        type="search"
        autoFocus
        className="[&::-webkit-search-cancel-button]:appearance-none"
        aria-label={t("wit.quote.search")}
        placeholder={t("wit.quote.search")}
        value={q}
        onChange={(e) => setQ(e.target.value)}
        onKeyDown={keys}
      />
      {hits?.length === 0 && (
        <p className="px-2.5 py-2 text-muted-foreground text-sm">{t("wit.quote.none")}</p>
      )}
      {hits?.map((h) => (
        <button
          key={h.id}
          type="button"
          className="flex flex-col items-start rounded-md px-2.5 py-1.5 text-left hover:bg-accent focus-visible:bg-accent focus-visible:outline-none"
          onClick={() => onPick(h)}
          onKeyDown={keys}
        >
          <span className="w-full truncate">{subjectOf(h, t)}</span>
          <span className="w-full truncate text-muted-foreground text-sm">{h.from}</span>
        </button>
      ))}
    </div>
  );
}

/** The mail quoted into the next question, as chips over the input; ✕ drops one. */
export function QuoteChips({
  items,
  onRemove,
  t,
}: {
  items: MailHit[];
  onRemove(id: number): void;
  t: T;
}) {
  return (
    <div className="flex w-full flex-wrap gap-1.5">
      {items.map((h) => (
        <Badge
          key={h.id}
          variant="secondary"
          className="h-6 max-w-full gap-1 rounded-sm pr-1 font-normal text-sm"
        >
          <RiMailLine className="size-3.5 text-muted-foreground" />
          <span className="truncate">{subjectOf(h, t)}</span>
          <button
            type="button"
            aria-label={t("wit.quote.remove", subjectOf(h, t))}
            className="rounded-sm text-muted-foreground hover:text-foreground"
            onClick={() => onRemove(h.id)}
          >
            <RiCloseLine className="size-3" />
          </button>
        </Badge>
      ))}
    </div>
  );
}
