import { RiCloseLine } from "@remixicon/react";
import { useRef } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { T } from "@/shared/i18n";
import { invalid } from "./address";

/**
 * An address field: its addresses as chips (red when invalid), each with a × taking it out,
 * before an input whose text becomes addresses on Enter, a comma, or leaving it. Never while an IME
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
          variant={invalid([addr]).length ? "destructive" : "secondary"}
          className="h-6 pr-0.5"
        >
          {addr}
          <Button
            variant="ghost"
            size="icon-xs"
            className="size-5"
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
        className="min-w-30 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
        placeholder={placeholder}
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
