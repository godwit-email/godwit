import { cn } from "cn";
import type { ComponentProps } from "react";
import { Badge as Base } from "@/components/ui/badge";

type Props = Omit<ComponentProps<typeof Base>, "variant"> & {
  /**
   * neutral = the tint; danger = an invalid address. warning (the pending mark) and outline (a
   * hairline, a host) are the Wit page's.
   */
  tone?: "neutral" | "danger" | "warning" | "outline";
};

const tones = {
  neutral: "bg-(--tint-2) text-foreground",
  danger: "bg-(--danger-bg) text-(--danger-text)",
  warning: "bg-(--warning-bg) text-(--warning-text)",
  outline: "border-[0.5px] border-border bg-transparent text-foreground",
} satisfies Record<NonNullable<Props["tone"]>, string>;

/** The design's Badge: 20 high, 6 across, text-xs, md radius; children may trail (a remove button). */
export function Badge({ tone = "neutral", className, ...props }: Props) {
  return (
    <Base
      variant="secondary"
      className={cn("h-5 rounded-md px-1.5 font-normal text-xs", tones[tone], className)}
      {...props}
    />
  );
}
