import { RiCloseLine } from "@remixicon/react";
import { useRef } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { T } from "@/shared/i18n";
import { display, invalid } from "./address";

/**
 * An address field: its addresses as chips (red when invalid) showing the display name, the address
 * in a tooltip, and a × taking it out on hover or focus; then an input whose text becomes addresses on Enter, a comma, or leaving it. Never while an IME
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
      {list.map((addr) => (
        <Badge
          key={addr}
          data-testid="chip"
          variant={invalid([addr]).length ? "destructive" : "secondary"}
          className="h-auto rounded-full px-2 py-0.5 font-normal text-md leading-4"
        >
          <Tooltip>
            <TooltipTrigger render={<span />}>{display(addr)}</TooltipTrigger>
            <TooltipContent>{addr}</TooltipContent>
          </Tooltip>
          {/* Out of the layout until the chip is hovered or the × focused: the chip is just the name. */}
          <Button
            variant="ghost"
            size="icon-xs"
            className="-mr-1 size-4 rounded-full not-group-hover/badge:not-focus-visible:sr-only"
            aria-label={t("compose.remove", addr)}
            disabled={frozen}
            onClick={() => !frozen && onRemove(addr)}
          >
            <RiCloseLine />
          </Button>
        </Badge>
      ))}
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
