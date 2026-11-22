import { RiErrorWarningLine, RiInformationLine } from "@remixicon/react";
import { cn } from "cn";
import type { ReactNode } from "react";
import { Alert as Base } from "@/components/ui/alert";

type Tone = "warning" | "danger" | "info";

// Alert's colours are the tone's three variables (webui.ThemeVars): the fill, a hairline edge and the text.
const tones: Record<Tone, string> = {
  warning: "border-(--warning-border) bg-(--warning-bg) text-(--warning-text)",
  danger: "border-(--danger-border) bg-(--danger-bg) text-(--danger-text)",
  info: "border-(--info-border) bg-(--info-bg) text-(--info-text)",
};

/**
 * The design's Alert: a notice to read before acting (a refused send, a plugin's warning, a draft
 * a program wrote). Its tone's mark of 16 level with the first line however tall the action is,
 * the lines of text in a column, and at most one action at the end, centred. Its children are the
 * lines; wrap each title in `font-medium`. `role` is "alert" (assertive) unless a notice that
 * does not need to interrupt says "status".
 */
export function Alert({
  tone,
  action,
  className,
  role,
  children,
}: {
  tone: Tone;
  action?: ReactNode;
  className?: string;
  role?: "alert" | "status";
  children: ReactNode;
}) {
  const Mark = tone === "info" ? RiInformationLine : RiErrorWarningLine;
  return (
    <Base
      role={role ?? "alert"}
      className={cn(
        "flex w-auto items-center gap-3 rounded-xl border-[0.5px] px-4 py-3 text-sm",
        tones[tone],
        className,
      )}
    >
      <div className="flex min-w-0 flex-1 items-start gap-3">
        <Mark className="mt-0.5 size-4 shrink-0" aria-hidden />
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">{children}</div>
      </div>
      {action && <div className="shrink-0">{action}</div>}
    </Base>
  );
}
