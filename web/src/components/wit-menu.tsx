import {
  type ComponentProps,
  cloneElement,
  type KeyboardEvent,
  type ReactElement,
  type ReactNode,
  useRef,
  useState,
} from "react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type { MenuEntry } from "@/mygo";

/** Shows entries as the system's menu at x, y of the page; the id chosen, "" for none. */
export type PopupMenu = (entries: MenuEntry[], x: number, y: number) => Promise<string>;

export const separator: MenuEntry = { id: "", label: "", separator: true };

/** Pops entries up under el's lower left corner; the id chosen, "" for none. */
export function popupUnder(popup: PopupMenu, el: Element, entries: MenuEntry[]) {
  const box = el.getBoundingClientRect();
  return popup(entries, Math.round(box.left), Math.round(box.bottom));
}

function WebEntries({ entries, onChoose }: { entries: MenuEntry[]; onChoose(id: string): void }) {
  const out: ReactNode[] = [];
  for (let i = 0; i < entries.length; i++) {
    const e = entries[i];
    const badge = e.badge && <DropdownMenuShortcut>{e.badge}</DropdownMenuShortcut>;
    if (e.separator) out.push(<DropdownMenuSeparator key={`separator-${i}`} />);
    else if (e.header) {
      // a heading names the entries up to the next separator or heading
      const end = entries.findIndex((x, k) => k > i && (x.separator || x.header));
      const group = entries.slice(i + 1, end < 0 ? undefined : end);
      out.push(
        <DropdownMenuGroup key={`header-${i}`}>
          <DropdownMenuLabel>{e.label}</DropdownMenuLabel>
          <WebEntries entries={group} onChoose={onChoose} />
        </DropdownMenuGroup>,
      );
      i += group.length;
    } else if (e.items?.length)
      out.push(
        <DropdownMenuSub key={e.id}>
          <DropdownMenuSubTrigger className="[&>svg]:ml-0">
            {e.label}
            {badge}
          </DropdownMenuSubTrigger>
          <DropdownMenuSubContent>
            <WebEntries entries={e.items} onChoose={onChoose} />
          </DropdownMenuSubContent>
        </DropdownMenuSub>,
      );
    else if (e.radio) {
      // a run of radio items is one group
      const run = entries.slice(i).findIndex((x) => !x.radio);
      const group = entries.slice(i, run < 0 ? undefined : i + run);
      out.push(
        <DropdownMenuRadioGroup
          key={e.id}
          value={group.find((x) => x.checked)?.id ?? ""}
          onValueChange={onChoose}
        >
          {group.map((x) => (
            <DropdownMenuRadioItem closeOnClick key={x.id} value={x.id} disabled={x.disabled}>
              {x.label}
              {x.badge && <DropdownMenuShortcut>{x.badge}</DropdownMenuShortcut>}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>,
      );
      i += group.length - 1;
    } else
      out.push(
        <DropdownMenuItem key={e.id} disabled={e.disabled} onClick={() => onChoose(e.id)}>
          {e.label}
          {badge}
        </DropdownMenuItem>,
      );
  }
  return out;
}

/**
 * A button that opens a menu of entries (read when it opens) and tells which id was chosen. In the
 * app it is the system's menu (`popup`): the button stays focusable, opens with Enter, Space or
 * ArrowDown, and has the focus back once the menu closes. Without `popup`, as in the tests and the
 * preview, a web menu of the same entries.
 */
export function WitMenu({
  trigger,
  entries,
  onChoose,
  popup,
  onError,
  side,
  align,
}: {
  trigger: ReactElement<ComponentProps<"button">>;
  entries(): MenuEntry[] | Promise<MenuEntry[]>;
  onChoose(id: string): void;
  popup?: PopupMenu;
  onError?(e: unknown): void;
  side?: "top" | "bottom";
  align?: "start" | "end";
}) {
  const button = useRef<HTMLButtonElement>(null);
  const showing = useRef(false);
  const [list, setList] = useState<MenuEntry[]>([]);
  if (!popup)
    return (
      <DropdownMenu
        onOpenChange={(open) => open && Promise.resolve(entries()).then(setList, onError)}
      >
        <DropdownMenuTrigger render={trigger} />
        <DropdownMenuContent side={side} align={align} className="max-h-80 min-w-45">
          <WebEntries entries={list} onChoose={onChoose} />
        </DropdownMenuContent>
      </DropdownMenu>
    );
  const show = async () => {
    const el = button.current;
    if (!el || showing.current) return;
    showing.current = true;
    try {
      const id = await popupUnder(popup, el, await entries());
      el.focus();
      if (id) onChoose(id);
    } catch (e) {
      onError?.(e);
    } finally {
      showing.current = false;
    }
  };
  return cloneElement(trigger, {
    ref: button,
    onClick: (e) => {
      trigger.props.onClick?.(e);
      void show();
    },
    onKeyDown: (e: KeyboardEvent<HTMLButtonElement>) => {
      trigger.props.onKeyDown?.(e);
      if (e.key !== "ArrowDown") return;
      e.preventDefault();
      void show();
    },
    "aria-haspopup": "menu",
  });
}
