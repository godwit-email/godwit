import { RiCloseLine } from "@remixicon/react";
import { Button } from "@/components/ui/button";
import type { Attachment } from "@/mygo";
import type { T } from "@/shared/i18n";
import { byteSize } from "./size";

/** The draft's attachments, each removable, and the names refused for size. */
export function Attachments({
  items,
  tooLarge,
  frozen,
  onRemove,
  t,
}: {
  items: Attachment[];
  tooLarge: string[];
  frozen: boolean;
  onRemove(id: number): void;
  t: T;
}) {
  return (
    <div className="flex flex-col gap-1">
      {items.map((a) => (
        <div key={a.id} className="flex items-center gap-2 text-sm">
          <span className="truncate">{a.filename}</span>
          <span className="text-muted-foreground">{byteSize(a.size)}</span>
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label={t("compose.remove", a.filename)}
            disabled={frozen}
            onClick={() => !frozen && onRemove(a.id)}
          >
            <RiCloseLine />
          </Button>
        </div>
      ))}
      {tooLarge.length > 0 && (
        <p role="alert" className="text-destructive text-sm">
          {tooLarge.map((n) => (
            <span key={n} className="mr-2 font-medium">
              {n}
            </span>
          ))}
          {t("compose.attach.too_large", byteSize(25 << 20))}
        </p>
      )}
    </div>
  );
}
