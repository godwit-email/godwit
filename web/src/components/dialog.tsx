import { AlertDialog as Primitive } from "@base-ui/react/alert-dialog";
import { cn } from "cn";
import type { ComponentProps } from "react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogDescription,
  AlertDialogOverlay,
  AlertDialogPortal,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";

// A dialog as the native kit.Dialog draws it (internal/kit/overlay.go), on shadcn's generated
// alert dialog (ADR 0012: generated files stay as generated, differences live here). Its
// measures come from the theme (webui.ThemeVars), so the native and the web dialogs share them.

export {
  AlertDialog as Dialog,
  AlertDialogAction as DialogAction,
  AlertDialogCancel as DialogCancel,
  AlertDialogDescription as DialogDescription,
};

/** kit.DialogSize: sm a confirmation, md a form, lg a larger form; never wider than the window less 32. */
const widths = {
  sm: "max-w-[min(var(--dialog-sm),calc(100%_-_32px))]",
  md: "max-w-[min(var(--dialog-md),calc(100%_-_32px))]",
  lg: "max-w-[min(var(--dialog-lg),calc(100%_-_32px))]",
};

/**
 * The panel: the centre at --dialog-centre of the window yet --dialog-min-top below its top, a
 * hairline border and ShadowX2l on the raised colour, over the scrim (alert: one question, the
 * alert's scrim; else a dialog's).
 */
export function DialogContent({
  size = "sm",
  alert = false,
  className,
  ...props
}: Primitive.Popup.Props & { size?: keyof typeof widths; alert?: boolean }) {
  return (
    <AlertDialogPortal>
      <AlertDialogOverlay
        className={cn(
          alert ? "bg-(--alert-scrim)" : "bg-(--scrim)",
          "supports-backdrop-filter:backdrop-blur-none",
        )}
      />
      <Primitive.Popup
        data-slot="alert-dialog-content"
        className={cn(
          "fixed top-(--dialog-centre) left-1/2 z-50 grid w-full -translate-x-1/2 translate-y-[max(-50%,calc(var(--dialog-min-top)_-_var(--dialog-centre)))] gap-4 rounded-xl border-[0.5px] bg-(--raised) p-4 text-popover-foreground shadow-(--shadow-dialog) outline-none duration-100 data-open:animate-in data-open:fade-in-0 data-open:zoom-in-95 data-closed:animate-out data-closed:fade-out-0 data-closed:zoom-out-95",
          widths[size],
          className,
        )}
        {...props}
      />
    </AlertDialogPortal>
  );
}

/** The title and the muted line under it, left-aligned. */
export function DialogHeader({ className, ...props }: ComponentProps<"div">) {
  return <div data-slot="alert-dialog-header" className={cn("grid gap-1", className)} {...props} />;
}

export function DialogTitle({ className, ...props }: ComponentProps<typeof AlertDialogTitle>) {
  return <AlertDialogTitle className={cn("font-semibold", className)} {...props} />;
}

/** The footer bar: the buttons at the end, 12 by 16 on the sidebar's colour under a hairline. */
export function DialogFooter({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      data-slot="alert-dialog-footer"
      className={cn(
        "-mx-4 -mb-4 flex items-center justify-end gap-2 rounded-b-xl border-t-[0.5px] bg-sidebar px-4 py-3",
        className,
      )}
      {...props}
    />
  );
}
