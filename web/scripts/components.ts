// Regenerates the shadcn components (Base UI flavour) untouched; never edit src/components/ui by hand.
// web:check reruns this and fails on any diff, so registry drift is a deliberate regenerate-and-commit.
export {};

const SHADCN = "4.21.4";
const components = [
  "button",
  "input",
  "textarea",
  "label",
  "alert",
  "toggle",
  "toggle-group",
  "popover",
  "tooltip",
  "separator",
  "select",
  "badge",
  "alert-dialog",
  "sonner",
  "message",
  "message-scroller",
  "bubble",
  "input-group",
  "marker",
  "collapsible",
  "card",
  "hover-card",
  "dropdown-menu",
  "trace",
  "progress",
];

const p = Bun.spawn(["bunx", `shadcn@${SHADCN}`, "add", "--yes", "--overwrite", ...components], {
  stdio: ["inherit", "inherit", "inherit"],
});
process.exit(await p.exited);
