import { RiCloseLine } from "@remixicon/react";
import { Button } from "@/components/ui/button";
import type { Attachment } from "@/mygo";
import type { T } from "@/shared/i18n";
import { byteSize } from "./size";

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
              className="flex h-7 max-w-full items-center gap-1.5 rounded-full border pr-0.5 pl-2.5 text-sm"
            >
              <span className="truncate">{a.filename}</span>
              <span className="shrink-0 text-muted-foreground">{byteSize(a.size)}</span>
              <Button
                variant="ghost"
                size="icon-xs"
                className="rounded-full"
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
        <p role="alert" className="text-base text-destructive">
          {tooLarge.map((n) => (
            <span key={n} className="mr-2 font-medium">
              {n}
            </span>
          ))}
          {t("compose.attach.too_large", byteSize(25 << 20))}
        </p>
      )}
      {failed.length > 0 && (
        <p role="alert" className="text-base text-destructive">
          {t("compose.attach.failed", failed.join(", "))}
        </p>
      )}
    </div>
  );
}
