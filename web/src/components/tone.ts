// A button on a colored notice follows it, as the native kit's ButtonOpts.Tone: a fill a step
// deeper than the notice's, under its text, no outline; deeper still on hover. Laid over the
// default Button (src/components/ui is generated: never edited), whose colours cn replaces.
export const toneButton = {
  warning:
    "bg-[color-mix(in_srgb,var(--warning-bg),var(--warning-border))] text-(--warning-text) hover:bg-(--warning-border)",
  danger: "bg-destructive/20 text-destructive hover:bg-destructive/30",
  info: "bg-blue-100 text-blue-700 hover:bg-blue-200 dark:bg-blue-900/60 dark:text-blue-300 dark:hover:bg-blue-800/70",
} as const;
