import { Separator as SeparatorPrimitive } from "@base-ui/react/separator"
import { cn } from "cn"

function Separator({
  className,
  orientation = "horizontal",
  ...props
}: SeparatorPrimitive.Props) {
  return (
    <SeparatorPrimitive
      data-slot="separator"
      orientation={orientation}
      className={cn(
        "shrink-0 data-horizontal:w-full data-horizontal:border-t-[0.5px] data-vertical:self-stretch data-vertical:border-l-[0.5px]",
        className
      )}
      {...props}
    />
  )
}

export { Separator }
