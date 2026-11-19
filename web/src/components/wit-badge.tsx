import { cn } from "cn";
import type { ComponentProps } from "react";
import { Badge } from "@/components/ui/badge";

type Props = Omit<ComponentProps<typeof Badge>, "variant"> & {
  /** warning = the pending mark; neutral = a plain tint; outline = a hairline (a host). */
  look?: "neutral" | "warning" | "outline";
};

const looks = {
  neutral: "bg-(--hover-tint) text-foreground",
  warning: "bg-(--warning-bg) text-(--warning-text)",
  outline: "border-[0.5px] border-border bg-transparent text-foreground",
} satisfies Record<NonNullable<Props["look"]>, string>;

/** The design's Badge on the Wit page: a tint, 4 radius, 6 across; outline for a host. */
export function WitBadge({ look = "neutral", className, ...props }: Props) {
  return (
    <Badge
      variant="secondary"
      className={cn("rounded-sm px-1.5 font-normal", looks[look], className)}
      {...props}
    />
  );
}
