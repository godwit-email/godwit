import { Checkbox as CheckboxPrimitive } from "@base-ui/react/checkbox";
import { RiCheckLine } from "@remixicon/react";
import { cn } from "cn";

/** The design's checkbox: a 16 square, Primary when checked; the label that names it wraps it. */
export function WitCheckbox({ className, ...props }: CheckboxPrimitive.Root.Props) {
  return (
    <CheckboxPrimitive.Root
      data-slot="wit-checkbox"
      className={cn(
        "flex size-4 shrink-0 cursor-pointer items-center justify-center rounded-sm border border-input bg-background outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring data-checked:border-primary data-checked:bg-primary data-checked:text-primary-foreground data-disabled:cursor-default data-disabled:opacity-50",
        className,
      )}
      {...props}
    >
      <CheckboxPrimitive.Indicator className="flex items-center justify-center">
        <RiCheckLine className="size-3" />
      </CheckboxPrimitive.Indicator>
    </CheckboxPrimitive.Root>
  );
}
