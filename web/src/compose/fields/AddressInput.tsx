import { RiCloseLine } from "@remixicon/react";
import { useEffect, useId, useRef, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { ContactHit } from "@/mygo";
import type { T } from "@/shared/i18n";
import { addrKey, display, invalid } from "./address";

/**
 * An address field: its addresses as chips, each labelled with the whole entry, showing the display
 * name with the address in a tooltip (an invalid one red, as typed), and a × taking it out on hover or focus; then an input whose text becomes addresses on Enter, a comma, or leaving it. Never while an IME
 * composes: the text is half-typed until its final input.
 *
 * With `onSearch`, what is typed lists matching contacts under the input (a combobox, as the quote
 * picker): ArrowUp / ArrowDown move, Enter or a click commits the contact, Escape closes. The first is
 * active, unless the text is an address already: then Enter commits the text as typed.
 */
export function AddressInput({
  id,
  list,
  pending,
  onPending,
  onCommit,
  onRemove,
  placeholder,
  onSearch,
  frozen = false,
  t,
}: {
  id: string;
  list: string[];
  pending: string;
  onPending(text: string): void;
  /** Makes `text` addresses and empties the input. */
  onCommit(text: string): void;
  onRemove(addr: string): void;
  placeholder: string;
  onSearch?(query: string): Promise<ContactHit[]>;
  /** Read-only, nothing committed or removed: an operation is running. */
  frozen?: boolean;
  t: T;
}) {
  const composing = useRef(false);
  const dismissed = useRef<string>(undefined); // the text Escape closed the list on: its hits stay away
  const [compositionEnds, setCompositionEnds] = useState(0);
  // the hits and the text they are for: Enter picks one only while that is still what is typed
  const [found, setFound] = useState<{ q: string; hits: ContactHit[] }>({ q: "", hits: [] });
  const hits = found.hits;
  const setHits = (h: ContactHit[]) => setFound({ q: "", hits: h });
  const [active, setActive] = useState(-1);
  const listId = useId();
  // a comma outside quotes ends an address
  const ends = (v: string) => v.replace(/"(?:[^"\\]|\\.)*("|$)/g, "").includes(",");
  const have = list.map(addrKey).join("\n"); // a string: the list is a new array on every render
  useEffect(() => {
    void compositionEnds; // a composition's end searches its final text
    if (dismissed.current !== pending) dismissed.current = undefined; // other text: a new search
    if (!onSearch || frozen || composing.current || !pending.trim()) {
      setFound({ q: "", hits: [] });
      return;
    }
    let live = true; // only the latest text's hits show; the last ones stay until then
    const taken = new Set(have.split("\n"));
    const timer = setTimeout(
      () =>
        onSearch(pending).then(
          (h) => {
            if (!live || dismissed.current === pending) return;
            setFound({ q: pending, hits: h.filter((c) => !taken.has(addrKey(c.address))) });
            setActive(invalid([pending]).length ? 0 : -1);
          },
          () => live && setFound({ q: "", hits: [] }),
        ),
      150, // once the typing pauses, as the quote picker
    );
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [pending, onSearch, frozen, have, compositionEnds]);
  const pick = (h: ContactHit) => {
    setHits([]);
    if (!frozen) onCommit(h.entry);
  };
  const open = hits.length > 0;
  const option = (i: number) => `${listId}-${i}`;
  return (
    <div className="relative flex min-w-0 flex-1 flex-wrap items-center gap-1.5">
      {list.map((addr) => {
        const bad = invalid([addr]).length > 0;
        return (
          <Badge
            key={addr}
            data-testid="chip"
            role="group"
            aria-label={addr}
            variant={bad ? "destructive" : "secondary"}
            className="h-auto px-2 py-0.5 text-sm leading-4"
          >
            <Tooltip>
              {/* invalid: the entry as typed, so what is wrong shows */}
              <TooltipTrigger render={<span />}>{bad ? addr : display(addr)}</TooltipTrigger>
              <TooltipContent>{addr}</TooltipContent>
            </Tooltip>
            {/* In the flow at no width until the chip is hovered or the × focused: the chip is just
                the name, and both directions animate (an absolutely hidden × would snap back). */}
            <Button
              variant="ghost"
              size="icon-xs"
              className="-ml-1 h-4 w-0 shrink-0 overflow-hidden rounded-full opacity-0 group-hover/badge:ml-0 group-hover/badge:w-4 group-hover/badge:opacity-100 focus-visible:ml-0 focus-visible:w-4 focus-visible:opacity-100"
              aria-label={t("compose.remove", addr)}
              disabled={frozen}
              onClick={() => !frozen && onRemove(addr)}
            >
              <RiCloseLine />
            </Button>
          </Badge>
        );
      })}
      <input
        id={id}
        role="combobox"
        aria-autocomplete="list"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        aria-activedescendant={open && active >= 0 ? option(active) : undefined}
        className="h-5.25 min-w-30 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
        placeholder={list.length ? undefined : placeholder}
        value={pending}
        readOnly={frozen}
        onChange={(e) => {
          if (frozen) return;
          const v = e.target.value;
          if (!composing.current && ends(v)) onCommit(v);
          else onPending(v);
        }}
        onCompositionStart={() => {
          composing.current = true;
        }}
        onCompositionEnd={() => {
          composing.current = false; // the text stays pending: the next input, Enter or blur commits it
          setCompositionEnds((n) => n + 1);
        }}
        onKeyDown={(e) => {
          if (e.nativeEvent.isComposing) return;
          const n = hits.length;
          if (n && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
            e.preventDefault();
            setActive((a) => (e.key === "ArrowDown" ? (a + 1) % n : a <= 0 ? n - 1 : a - 1));
          } else if (e.key === "Escape") {
            dismissed.current = pending;
            if (!n) return;
            e.preventDefault();
            e.stopPropagation(); // closes the list, nothing around it
            setHits([]);
          } else if (e.key === "Enter") {
            e.preventDefault();
            if (n && active >= 0 && found.q === pending) pick(hits[active]);
            else if (!frozen) onCommit(pending);
          }
        }}
        onBlur={() => {
          setHits([]);
          if (pending && !frozen && !composing.current) onCommit(pending);
        }}
      />
      {open && (
        <div
          role="listbox"
          id={listId}
          aria-label={t("shell.nav.contacts")}
          className="absolute top-full left-0 z-50 mt-2 flex w-80 max-w-full flex-col rounded-lg border border-glass-border bg-glass-bg p-1.5 text-popover-foreground shadow-md backdrop-blur-2xl"
        >
          {hits.map((h, i) => (
            <div
              key={h.address}
              id={option(i)}
              role="option"
              tabIndex={-1}
              aria-selected={i === active}
              className="flex cursor-default flex-col items-start rounded-md px-2.5 py-1.5 text-left aria-selected:bg-accent"
              // the focus stays in the input: leaving it would commit the half-typed text
              onMouseDown={(e) => e.preventDefault()}
              onMouseEnter={() => setActive(i)}
              onClick={() => pick(h)}
              onKeyDown={() => {}}
            >
              {h.name && <span className="w-full truncate">{h.name}</span>}
              <span
                className={
                  h.name ? "w-full truncate text-muted-foreground text-xs" : "w-full truncate"
                }
              >
                {h.address}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
