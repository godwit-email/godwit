import { RiSparkling2Line } from "@remixicon/react";
import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { T } from "@/shared/i18n";

/**
 * ⌘K's popover under the selection: what to change (Enter or 生成 ↵ submits the trimmed text; an
 * empty one does nothing), Esc closes. A pointer down outside closes it too.
 */
export function CmdK({
  at,
  onSubmit,
  onClose,
  t,
}: {
  at: { left: number; top: number };
  onSubmit(instruction: string): void;
  onClose(): void;
  t: T;
}) {
  const [text, setText] = useState("");
  const box = useRef<HTMLFormElement>(null);
  useEffect(() => {
    const down = (e: PointerEvent) => {
      if (!box.current?.contains(e.target as Node)) onClose();
    };
    document.addEventListener("pointerdown", down);
    return () => document.removeEventListener("pointerdown", down);
  }, [onClose]);

  return (
    <form
      ref={box}
      style={{ left: at.left, top: at.top }}
      className="fixed z-50 flex w-96 flex-col gap-2 rounded-lg border bg-popover p-2 shadow-md"
      onSubmit={(e) => {
        e.preventDefault();
        const s = text.trim();
        if (s) onSubmit(s);
      }}
      onKeyDown={(e) => {
        // Enter that commits an IME composition is not a submit.
        if (e.key === "Enter" && (e.nativeEvent.isComposing || e.keyCode === 229))
          e.preventDefault();
        if (e.key === "Escape") {
          e.preventDefault();
          onClose();
        }
      }}
    >
      <div className="flex items-center gap-2">
        <RiSparkling2Line className="size-4 shrink-0 text-primary" />
        <Input
          autoFocus
          aria-label={t("wit.compose.cmdk.input")}
          placeholder={t("wit.compose.cmdk.input")}
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
      </div>
      <div className="flex items-center gap-2">
        <span className="flex-1 text-muted-foreground text-sm">{t("wit.compose.cmdk")}</span>
        <Button type="button" size="sm" variant="outline" onClick={onClose}>
          {t("wit.compose.cmdk.esc")}
        </Button>
        <Button type="submit" size="sm">
          {t("wit.compose.cmdk.go")}
        </Button>
      </div>
    </form>
  );
}
