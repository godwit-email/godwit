import { RiCloseLine } from "@remixicon/react";
import { Button } from "@/components/button";
import type { Attachment } from "@/mygo";
import type { T } from "@/shared/i18n";
import { byteSize } from "./size";

/** The tile's label: the file's extension, at most four letters. */
const kind = (name: string) => {
  const dot = name.lastIndexOf(".");
  return dot < 0 ? "" : name.slice(dot + 1, dot + 5);
};

/** The draft's attachments, each removable, the names refused for size and those that failed. */
export function Attachments({
  items,
  tooLarge,
  failed = [],
  frozen,
  onRemove,
  t,
}: {
  items: Attachment[];
  tooLarge: string[];
  failed?: string[];
  frozen: boolean;
  onRemove(id: number): void;
  t: T;
}) {
  return (
    <div className="flex flex-col gap-2">
      {items.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {items.map((a) => (
            <div
              key={a.id}
              className="flex w-70.5 max-w-full items-center gap-1.5 rounded-lg border bg-background px-2.5 py-2"
            >
              <span className="flex h-8.5 w-7 shrink-0 items-center justify-center rounded-md bg-(--hover-tint) text-muted-foreground text-xs uppercase">
                {kind(a.filename)}
              </span>
              <span className="ml-1 flex min-w-0 flex-1 flex-col">
                <span className="truncate text-sm">{a.filename}</span>
                <span className="text-muted-foreground text-xs">{byteSize(a.size)}</span>
              </span>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={t("compose.remove", a.filename)}
                disabled={frozen}
                onClick={() => !frozen && onRemove(a.id)}
              >
                <RiCloseLine />
              </Button>
            </div>
          ))}
        </div>
      )}
      {tooLarge.length > 0 && (
        <p role="alert" className="text-xs text-destructive">
          {tooLarge.map((n) => (
            <span key={n} className="mr-2 font-medium">
              {n}
            </span>
          ))}
          {t("compose.attach.too_large", byteSize(25 << 20))}
        </p>
      )}
      {failed.length > 0 && (
        <p role="alert" className="text-xs text-destructive">
          {t("compose.attach.failed", failed.join(", "))}
        </p>
      )}
    </div>
  );
}
