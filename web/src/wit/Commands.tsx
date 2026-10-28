import { RiCheckLine } from "@remixicon/react";
import {
  type KeyboardEvent,
  type ReactNode,
  type Ref,
  useEffect,
  useId,
  useImperativeHandle,
  useState,
} from "react";
import type { T } from "@/shared/i18n";
import type { PickerKeys } from "./Quote";

/**
 * A command the input's / menu offers: its group's heading, its icon, what it is called, what it
 * stands at now (value) or does (hint). One with options opens them in the menu's place instead
 * of running; an option the command stands at is checked. A disabled one shows, greyed, and does
 * nothing (its hint says why).
 */
export type Command = {
  key: string;
  group: string;
  icon?: ReactNode;
  label: string;
  value?: string;
  hint?: string;
  checked?: boolean;
  disabled?: boolean;
  options?: Command[];
  run?(): void;
};

/**
 * The input's / menu: a / at the input's start opens it, the text after it filters the commands
 * (by key or label), grouped under their headings; a command's options replace them, Escape going
 * back. The textarea forwards its keys (`keys`): ArrowUp / ArrowDown move, Enter or Tab picks,
 * Escape closes. Built from the quote picker's look.
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
  // the command whose options show, by key: its options read from the commands as they are now
  // (a model list that arrives after it opened shows)
  const [openKey, setOpen] = useState<string>();
  const open = commands.find((c) => c.key === openKey);
  const q = query.toLowerCase();
  const top = commands.filter(
    (c) => !q || c.key.toLowerCase().includes(q) || c.label.toLowerCase().includes(q),
  );
  const shown = open?.options ?? top;
  const [active, setActive] = useState(0);
  // biome-ignore lint/correctness/useExhaustiveDependencies: a new query starts at the top
  useEffect(() => {
    setActive(0);
    setOpen(undefined);
  }, [query]);
  const id = useId();
  const pick = (c: Command) => {
    if (c.disabled) return;
    if (c.options) {
      setOpen(c.key);
      setActive(
        Math.max(
          0,
          c.options.findIndex((o) => o.checked),
        ),
      );
    } else c.run?.();
  };
  const key = (e: KeyboardEvent) => {
    const n = shown.length;
    switch (e.key) {
      case "Escape":
        if (!open) {
          onClose();
          break;
        }
        // back to the commands, on the one it came from
        setActive(
          Math.max(
            0,
            top.findIndex((c) => c.key === open.key),
          ),
        );
        setOpen(undefined);
        break;
      case "ArrowDown":
      case "ArrowUp":
        if (!n) return false;
        setActive((a) => (a + (e.key === "ArrowDown" ? 1 : n - 1)) % n);
        break;
      case "Enter":
      case "Tab":
        if (e.nativeEvent.isComposing || !shown[active]) return false;
        pick(shown[active]);
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
      className="absolute bottom-full left-0 z-3 mb-2 flex max-h-80 w-96 max-w-full flex-col gap-0.5 overflow-y-auto rounded-xl border border-glass-border bg-glass-bg p-1 text-popover-foreground shadow-2xl backdrop-blur-2xl ring-1 ring-glass-ring outline-none"
    >
      {shown.length === 0 ? (
        <p className="px-2.5 py-2 text-muted-foreground text-sm">{t("wit.cmd.none")}</p>
      ) : (
        <div
          role="listbox"
          id={id}
          aria-label={open?.label ?? t("wit.cmd.menu")}
          className="flex flex-col"
        >
          {shown.map((c, i) => (
            <div key={c.key} className="flex flex-col">
              {(i === 0 || shown[i - 1].group !== c.group) && (
                <p className="px-2.5 pt-1.5 pb-0.5 text-muted-foreground text-xs">
                  {open?.label ?? c.group}
                </p>
              )}
              <div
                id={`${id}-${i}`}
                role="option"
                tabIndex={-1}
                aria-selected={i === active}
                aria-disabled={c.disabled || undefined}
                className="flex cursor-default items-center gap-2.5 rounded-md px-2.5 py-1.5 aria-selected:bg-accent aria-disabled:opacity-50"
                // the focus stays in the textarea that drives the list
                onMouseDown={(e) => e.preventDefault()}
                onMouseEnter={() => setActive(i)}
                onClick={() => pick(c)}
                onKeyDown={key}
              >
                {c.icon && (
                  <span className="flex size-4 shrink-0 items-center justify-center text-muted-foreground [&_svg]:size-4">
                    {c.icon}
                  </span>
                )}
                <span className="shrink-0 font-medium text-sm">{c.label}</span>
                {c.value && (
                  <span className="shrink-0 text-muted-foreground text-sm">{c.value}</span>
                )}
                {c.hint && (
                  <span className="min-w-0 truncate text-muted-foreground text-sm">{c.hint}</span>
                )}
                {c.checked && <RiCheckLine className="ml-auto size-4 shrink-0" />}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
