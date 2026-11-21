import { RiSparkling2Line } from "@remixicon/react";
import type { ReactNode } from "react";

/**
 * Compose/Banner, Wit's kinds (writing, rewrite): a band across the whole card on the sidebar's
 * colour under a hairline, a sparkle, its text, and its buttons at the end.
 */
export function WitBanner({ text, children }: { text: string; children?: ReactNode }) {
  return (
    <div className="flex items-center gap-2 border-b-[0.5px] bg-sidebar px-4 py-2 text-sm">
      <RiSparkling2Line className="size-3.5 shrink-0" />
      <span className="flex-1">{text}</span>
      {children}
    </div>
  );
}

/**
 * Compose/Banner kind=program-draft (ADR 0043): at the card's top, the notice's info colours under
 * a hairline, the title over the help (two lines, 2 apart). No action.
 */
export function ProgramBanner({ title, help }: { title: string; help: string }) {
  return (
    <div className="flex flex-col gap-0.5 border-b-[0.5px] border-(--info-border) bg-(--info-bg) px-4 py-2 text-(--info-text) text-sm">
      <span className="font-medium">{title}</span>
      <span>{help}</span>
    </div>
  );
}
