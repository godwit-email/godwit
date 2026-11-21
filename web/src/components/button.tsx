import { cn } from "cn";
import type { ComponentProps } from "react";
import { Button as BaseButton } from "@/components/ui/button";

export type Look = "default" | "secondary" | "outline" | "ghost" | "link";
export type Tone = "neutral" | "danger" | "warning" | "info" | "success";
type Size = "xs" | "sm" | "md" | "lg" | "icon" | "icon-xs" | "icon-sm" | "icon-lg";

const idle = "disabled:text-(color:--idle)";
const danger =
  "hover:bg-(color:--danger-bg) active:bg-[color:color-mix(in_srgb,var(--danger-bg),var(--destructive)_12%)]";

// A colored outline: its border and text on Background, its fill on hover, halfway to the border pressed.
const outlineTone = {
  neutral: `border-input text-foreground shadow-xs hover:bg-muted active:bg-[color:color-mix(in_srgb,var(--muted),var(--foreground)_8%)] aria-expanded:bg-muted ${idle}`,
  danger: `border-(color:--danger-border) text-destructive ${danger} ${idle}`,
  warning: `border-(color:--warning-border) text-(color:--warning-text) hover:bg-(color:--warning-bg) active:bg-[color:color-mix(in_srgb,var(--warning-bg),var(--warning-border))] ${idle}`,
  info: `border-(color:--info-border) text-(color:--info-text) hover:bg-(color:--info-bg) active:bg-[color:color-mix(in_srgb,var(--info-bg),var(--info-border))] ${idle}`,
  success: `border-(color:--success-border) text-(color:--success-text) hover:bg-(color:--success-bg) active:bg-[color:color-mix(in_srgb,var(--success-bg),var(--success-border))] ${idle}`,
} satisfies Record<Tone, string>;

const ghostTone = {
  neutral: `text-foreground hover:bg-(color:--hover-tint) active:bg-(color:--tint-2) aria-expanded:bg-(color:--hover-tint) ${idle}`,
  danger: `text-destructive ${danger} ${idle}`,
} satisfies Partial<Record<Tone, string>>;

const looks = {
  default:
    "bg-primary text-primary-foreground hover:bg-[color:color-mix(in_srgb,var(--primary),var(--background)_10%)] active:bg-[color:color-mix(in_srgb,var(--primary),var(--background)_20%)]",
  secondary: `bg-(color:--hover-tint) text-foreground hover:bg-(color:--tint-2) active:bg-(color:--tint-3) ${idle}`,
  link: `h-auto rounded-none border-0 p-0 font-normal text-foreground underline underline-offset-4 hover:text-muted-foreground active:text-muted-foreground ${idle}`,
} satisfies Partial<Record<Look, string>>;

function lookClass(look: Look, tone: Tone, size: Size) {
  switch (look) {
    case "outline":
      return `bg-background ${outlineTone[tone]}`;
    case "ghost": {
      const toned = tone === "danger" ? ghostTone.danger : ghostTone.neutral;
      // a lone icon rests muted and turns foreground on hover
      return tone !== "danger" && size.startsWith("icon")
        ? cn(toned, "text-muted-foreground hover:text-foreground")
        : toned;
    }
    default:
      return looks[look];
  }
}

type Props = Omit<ComponentProps<typeof BaseButton>, "variant" | "size"> & {
  variant?: Look;
  /** Outline takes any tone and Ghost danger; with other looks a tone is neutral. */
  tone?: Tone;
  size?: Size;
};

/** Godwit's button: a look and a tone, as the native kit's, over the generated shadcn button's size. */
export function Button({
  variant = "default",
  tone = "neutral",
  size = "md",
  className,
  ...props
}: Props) {
  // Primary keeps the base's half opacity when disabled; every other look sets its own disabled text.
  const rest = variant === "default" ? "" : "disabled:opacity-100";
  return (
    <BaseButton
      variant={null}
      size={size === "md" ? "default" : size}
      className={cn(lookClass(variant, tone, size), rest, className)}
      {...props}
    />
  );
}
