import { RiCloseLine } from "@remixicon/react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { T } from "@/shared/i18n";
import { invalid } from "./address";

/**
 * An address field: its addresses as chips (red when invalid), each with a × taking it out,
 * before an input whose text becomes addresses on Enter, a comma, or leaving it.
 */
export function AddressInput({
  id,
  list,
  pending,
  onPending,
  onCommit,
  onRemove,
  placeholder,
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
  t: T;
}) {
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
            onClick={() => onRemove(addr)}
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
        onChange={(e) => {
          const v = e.target.value;
          // a comma outside quotes ends an address
          if (v.replace(/"(?:[^"\\]|\\.)*("|$)/g, "").includes(",")) onCommit(v);
          else onPending(v);
        }}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.nativeEvent.isComposing) {
            e.preventDefault();
            onCommit(pending);
          }
        }}
        onBlur={() => pending && onCommit(pending)}
      />
    </div>
  );
}
