import {
  type KeyboardEvent,
  type Ref,
  useEffect,
  useId,
  useImperativeHandle,
  useState,
} from "react";
import type { T } from "@/shared/i18n";
import type { PickerKeys } from "./Quote";

/** A command the input's / menu offers: its group's heading, what it is called, what it does. */
export type Command = { key: string; group: string; label: string; hint?: string; run(): void };

/**
 * The input's / menu: a / at the input's start opens it, the text after it filters the commands
 * (by key or label), grouped under their headings. The textarea forwards its keys (`keys`):
 * ArrowUp / ArrowDown move, Enter or Tab runs, Escape closes. Built from the quote picker's look.
 */
export function CommandPicker({
  commands,
  query,
  onClose,
  keys,
  t,
}: {
  commands: Command[];
  query: string;
  onClose(): void;
  keys?: Ref<PickerKeys>;
  t: T;
}) {
  const q = query.toLowerCase();
  const shown = commands.filter(
    (c) => !q || c.key.toLowerCase().includes(q) || c.label.toLowerCase().includes(q),
  );
  const [active, setActive] = useState(0);
  // biome-ignore lint/correctness/useExhaustiveDependencies: a new query starts at the top
  useEffect(() => setActive(0), [query]);
  const id = useId();
  const key = (e: KeyboardEvent) => {
    const n = shown.length;
    switch (e.key) {
      case "Escape":
        onClose();
        break;
      case "ArrowDown":
      case "ArrowUp":
        if (!n) return false;
        setActive((a) => (a + (e.key === "ArrowDown" ? 1 : n - 1)) % n);
        break;
      case "Enter":
      case "Tab":
        if (e.nativeEvent.isComposing || !shown[active]) return false;
        shown[active].run();
        break;
      default:
        return false;
    }
    e.preventDefault();
    e.stopPropagation();
    return true;
  };
  useImperativeHandle(keys, () => ({ key }));
  return (
    <div
      role="dialog"
      aria-label={t("wit.cmd.menu")}
      className="absolute bottom-full left-0 z-3 mb-2 flex max-h-80 w-80 max-w-full flex-col gap-0.5 overflow-y-auto rounded-lg border-[0.5px] bg-popover p-1 text-popover-foreground shadow-md"
    >
      {shown.length === 0 ? (
        <p className="px-2.5 py-2 text-muted-foreground text-sm">{t("wit.cmd.none")}</p>
      ) : (
        <div role="listbox" id={id} aria-label={t("wit.cmd.menu")} className="flex flex-col">
          {shown.map((c, i) => (
            <div key={c.key} className="flex flex-col">
              {(i === 0 || shown[i - 1].group !== c.group) && (
                <p className="px-2.5 pt-1.5 pb-0.5 text-muted-foreground text-xs">{c.group}</p>
              )}
              <div
                id={`${id}-${i}`}
                role="option"
                tabIndex={-1}
                aria-selected={i === active}
                className="flex cursor-default items-baseline gap-2 rounded-md px-2.5 py-1.5 aria-selected:bg-accent"
                // the focus stays in the textarea that drives the list
                onMouseDown={(e) => e.preventDefault()}
                onMouseEnter={() => setActive(i)}
                onClick={() => c.run()}
                onKeyDown={key}
              >
                <span className="shrink-0 font-medium text-sm">{c.label}</span>
                {c.hint && (
                  <span className="min-w-0 truncate text-muted-foreground text-xs">{c.hint}</span>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
