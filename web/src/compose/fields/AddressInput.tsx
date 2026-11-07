import { RiCloseLine } from "@remixicon/react";
import { useRef } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { T } from "@/shared/i18n";
import { display, invalid } from "./address";

/**
 * An address field: its addresses as chips, each labelled with the whole entry, showing the display
 * name with the address in a tooltip (an invalid one red, as typed), and a × taking it out on hover or focus; then an input whose text becomes addresses on Enter, a comma, or leaving it. Never while an IME
 * composes: the text is half-typed until its final input.
 */
export function AddressInput({
  id,
  list,
  pending,
  onPending,
  onCommit,
  onRemove,
  placeholder,
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
  /** Read-only, nothing committed or removed: an operation is running. */
  frozen?: boolean;
  t: T;
}) {
  const composing = useRef(false);
  // a comma outside quotes ends an address
  const ends = (v: string) => v.replace(/"(?:[^"\\]|\\.)*("|$)/g, "").includes(",");
  return (
    <div className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5">
      {list.map((addr) => {
        const bad = invalid([addr]).length > 0;
        return (
          <Badge
            key={addr}
            data-testid="chip"
            role="group"
            aria-label={addr}
            variant={bad ? "destructive" : "secondary"}
            className="h-auto rounded-full px-2 py-0.5 font-normal text-md leading-4"
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
        className="h-5.25 min-w-30 flex-1 bg-transparent text-md outline-none placeholder:text-muted-foreground"
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
        }}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.nativeEvent.isComposing) {
            e.preventDefault();
            if (!frozen) onCommit(pending);
          }
        }}
        onBlur={() => pending && !frozen && !composing.current && onCommit(pending)}
      />
    </div>
  );
}
