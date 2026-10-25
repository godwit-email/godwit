import { RiCloseLine, RiMailLine } from "@remixicon/react";
import {
  type KeyboardEvent,
  type Ref,
  useEffect,
  useId,
  useImperativeHandle,
  useState,
} from "react";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import type { MailHit } from "@/mygo";
import type { T } from "@/shared/i18n";

const subjectOf = (h: MailHit, t: T) => h.subject || t("mail.no_subject");

/** What the picker does with a key the textarea forwards (an @ query): true when it took it. */
export type PickerKeys = { key(e: KeyboardEvent): boolean };

/**
 * The quote picker (引用邮件): a search of the user's mail over the input, its hits a listbox.
 * From the + menu it has its own search box; from an @ in the text, `query` is what follows the @
 * and the textarea forwards its keys (`keys`). With nothing typed it offers the latest mail. ArrowUp / ArrowDown move, Enter picks, Escape
 * closes. Invented UI (the mockup shows only its entry point), built from the menu's look.
 */
export function QuotePicker({
  search,
  query,
  onPick,
  onClose,
  keys,
  t,
}: {
  search(q: string): Promise<MailHit[]>;
  /** The @ query; undefined: the picker's own search box. */
  query?: string;
  onPick(h: MailHit): void;
  onClose(): void;
  keys?: Ref<PickerKeys>;
  t: T;
}) {
  const own = query === undefined;
  const [typed, setTyped] = useState("");
  const q = own ? typed : query;
  const [hits, setHits] = useState<MailHit[]>();
  const [active, setActive] = useState(0);
  const id = useId();
  useEffect(() => {
    let live = true;
    const timer = setTimeout(
      () =>
        search(q).then(
          (h) => {
            if (!live) return;
            setHits(h);
            setActive(0);
          },
          () => live && setHits([]),
        ),
      q.trim() ? 150 : 0, // the latest mail at once; a search once the typing pauses
    );
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [q, search]);
  const key = (e: KeyboardEvent) => {
    const n = hits?.length ?? 0;
    switch (e.key) {
      case "Escape":
        onClose();
        break;
      case "ArrowDown":
      case "ArrowUp":
        if (!n) return false;
        setActive((a) => (a + (e.key === "ArrowDown" ? 1 : n - 1)) % n);
        break;
      case "Enter":
        if (e.nativeEvent.isComposing || !hits?.[active]) return false;
        onPick(hits[active]);
        break;
      default:
        return false;
    }
    e.preventDefault();
    e.stopPropagation();
    return true;
  };
  useImperativeHandle(keys, () => ({ key }));
  const option = (i: number) => `${id}-${i}`;
  // from an @, nothing shows until there is something to show (no empty box)
  if (!own && hits === undefined) return null;
  return (
    <div
      role="dialog"
      aria-label={t("wit.quote")}
      className="absolute bottom-full left-0 z-3 mb-2 flex w-80 max-w-full flex-col gap-1 rounded-lg border-[0.5px] bg-popover p-1 text-popover-foreground shadow-md"
    >
      {own && (
        <Input
          role="combobox"
          autoFocus
          aria-label={t("wit.quote.search")}
          aria-expanded={!!hits?.length}
          aria-controls={id}
          aria-activedescendant={hits?.length ? option(active) : undefined}
          placeholder={t("wit.quote.search")}
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          onKeyDown={key}
        />
      )}
      {hits?.length === 0 && (
        <p className="px-2.5 py-2 text-muted-foreground text-sm">{t("wit.quote.none")}</p>
      )}
      {!!hits?.length && !q.trim() && (
        <p className="px-2.5 pt-1 text-muted-foreground text-xs">{t("wit.quote.recent")}</p>
      )}
      {!!hits?.length && (
        <div role="listbox" id={id} aria-label={t("wit.quote")} className="flex flex-col">
          {hits.map((h, i) => (
            <div
              key={h.id}
              id={option(i)}
              role="option"
              tabIndex={-1}
              aria-selected={i === active}
              className="flex cursor-default flex-col items-start rounded-md px-2.5 py-1.5 text-left aria-selected:bg-accent"
              // the focus stays in the box (or the textarea) that drives the list
              onMouseDown={(e) => e.preventDefault()}
              onMouseEnter={() => setActive(i)}
              onClick={() => onPick(h)}
              onKeyDown={key}
            >
              <span className="w-full truncate">{subjectOf(h, t)}</span>
              <span className="w-full truncate text-muted-foreground text-sm">{h.from}</span>
            </div>
          ))}
        </div>
      )}
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
