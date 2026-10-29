import { RiSparkling2Line } from "@remixicon/react";
import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { T } from "@/shared/i18n";

/**
 * ⌘K's popover, under the selection's block at the body's width: what to change (Enter or 生成 ↵ submits the trimmed text; an
 * empty one does nothing), Esc closes. A pointer down or focus leaving it closes it too.
 */
export function CmdK({
  onSubmit,
  onClose,
  t,
}: {
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
      className="flex flex-col gap-1.5 rounded-xl border border-glass-border bg-glass-bg px-2.5 py-2 text-md leading-normal text-popover-foreground shadow-2xl backdrop-blur-2xl ring-1 ring-glass-ring outline-none"
      onSubmit={(e) => {
        e.preventDefault();
        const s = text.trim();
        if (s) onSubmit(s);
      }}
      onBlur={(e) => {
        // Focus leaving the popover (Tab) closes it, and with it the held selection.
        if (e.relatedTarget && !box.current?.contains(e.relatedTarget)) onClose(); // not a window switch
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
        <RiSparkling2Line className="size-3.5 shrink-0" />
        <Input
          autoFocus
          aria-label={t("wit.compose.cmdk.input")}
          placeholder={t("wit.compose.cmdk.input")}
          value={text}
          onChange={(e) => setText(e.target.value)}
          className="h-auto rounded-none border-0 bg-transparent p-0 text-md shadow-none focus-visible:ring-0 md:text-md dark:bg-transparent"
        />
      </div>
      <div className="flex items-center gap-2">
        <span className="flex-1 text-muted-foreground text-sm">{t("wit.compose.cmdk")}</span>
        <Button type="button" size="xs" variant="outline" onClick={onClose}>
          {t("wit.compose.cmdk.esc")}
        </Button>
        <Button type="submit" size="xs" className="px-2.5">
          {t("wit.compose.cmdk.go")}
        </Button>
      </div>
    </form>
  );
}
