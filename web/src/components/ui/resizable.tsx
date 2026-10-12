import { cn } from "cn"
import * as ResizablePrimitive from "react-resizable-panels"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"

function ResizablePanelGroup({
  className,
  ...props
}: ResizablePrimitive.GroupProps) {
  return (
    <ResizablePrimitive.Group
      data-slot="resizable-panel-group"
      className={cn(
        "flex h-full w-full aria-[orientation=vertical]:flex-col",
        className
      )}
      {...props}
    />
  )
}

function ResizablePanel({ ...props }: ResizablePrimitive.PanelProps) {
  return <ResizablePrimitive.Panel data-slot="resizable-panel" {...props} />
}

/**
 * The grip between two panels, as the native kit.Resizer: the 8 px gap
 * between the cards, whose middle shows a 3×56 line of the foreground at 20%
 * while hovered, dragged or focused; hovering the line names it (label).
 */
function ResizableHandle({
  label,
  className,
  ...props
}: ResizablePrimitive.SeparatorProps & { label?: string }) {
  const line = (
    <span className="block h-14 w-0.75 rounded-full bg-foreground/20 opacity-0 transition-opacity group-data-[separator=active]:opacity-100 group-data-[separator=focus]:opacity-100 group-data-[separator=hover]:opacity-100" />
  )
  return (
    <ResizablePrimitive.Separator
      data-slot="resizable-handle"
      aria-label={label}
      className={cn(
        "group flex w-2 shrink-0 items-center justify-center outline-hidden",
        className
      )}
      {...props}
    >
      {label ? (
        <Tooltip>
          <TooltipTrigger
            render={<span className="flex h-14 w-2 items-center justify-center" />}
          >
            {line}
          </TooltipTrigger>
          <TooltipContent side="right">{label}</TooltipContent>
        </Tooltip>
      ) : (
        line
      )}
    </ResizablePrimitive.Separator>
  )
}

export { ResizableHandle, ResizablePanel, ResizablePanelGroup }
