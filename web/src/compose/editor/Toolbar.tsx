import { upsertLink } from "@platejs/link";
import { toggleBulletedList, toggleNumberedList } from "@platejs/list-classic";
import {
  type RemixiconComponentType,
  RiBold,
  RiDoubleQuotesL,
  RiItalic,
  RiLink,
  RiListOrdered,
  RiListUnordered,
} from "@remixicon/react";
import type { SlateEditor, TRange } from "platejs";
import { useEditorReadOnly, useEditorRef, useEditorSelector } from "platejs/react";
import { type ReactElement, type ReactNode, useEffect, useRef } from "react";
import { Separator } from "@/components/ui/separator";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { safeHref } from "./html";
import { LinkPopover } from "./LinkPopover";

/** Looks up a native i18n key (Task 4's shared T replaces this). */
export type T = (key: string, ...args: unknown[]) => string;

type Key = "bold" | "italic" | "link" | "ul" | "ol" | "blockquote";

const items: {
  key: Key;
  label: string;
  Icon: RemixiconComponentType;
  run(e: SlateEditor): void;
}[] = [
  { key: "bold", label: "compose.bold", Icon: RiBold, run: (e) => e.tf.toggleMark("bold") },
  { key: "italic", label: "compose.italic", Icon: RiItalic, run: (e) => e.tf.toggleMark("italic") },
  { key: "link", label: "compose.link", Icon: RiLink, run: () => {} },
  {
    key: "ul",
    label: "compose.list",
    Icon: RiListUnordered,
    run: toggleBulletedList,
  },
  {
    key: "ol",
    label: "compose.list_ordered",
    Icon: RiListOrdered,
    run: toggleNumberedList,
  },
  {
    key: "blockquote",
    label: "compose.quote",
    Icon: RiDoubleQuotesL,
    run: (e) => e.tf.toggleBlock("blockquote"),
  },
];

// The pressed formats at the selection, space-separated (a string keeps the selector's equality cheap).
function active(e: SlateEditor): string {
  if (!e.selection) return "";
  const marks = e.api.marks() ?? {};
  const list = e.api.above({ match: { type: ["ul", "ol"] } })?.[0].type;
  return [
    marks.bold && "bold",
    marks.italic && "italic",
    e.api.some({ match: { type: "a" } }) && "link",
    list,
    e.api.some({ match: { type: "blockquote" } }) && "blockquote",
  ]
    .filter(Boolean)
    .join(" ");
}

/**
 * The format toggles (bold, italic, link, bulleted and numbered list, quote) for the Plate editor
 * around it, then the `attach` slot. The link toggle opens the LinkPopover (controlled by the editor, which ⌘K also opens).
 */
export function Toolbar(props: {
  t: T;
  linkOpen: boolean;
  onLinkOpenChange(open: boolean): void;
  attach?: ReactNode;
}) {
  const { t, linkOpen, onLinkOpenChange, attach } = props;
  const editor = useEditorRef();
  const readOnly = useEditorReadOnly();
  const value = useEditorSelector(active, []).split(" ").filter(Boolean);
  const saved = useRef<TRange | null>(null);

  useEffect(() => {
    if (linkOpen) saved.current = editor.selection;
  }, [linkOpen, editor]);

  const applyLink = (url: string) => {
    const href = safeHref(url);
    if (!href) return false;
    if (saved.current) editor.tf.select(saved.current);
    upsertLink(editor, { url: href });
    editor.tf.focus();
    return true;
  };

  const button = (it: (typeof items)[number]): ReactElement => (
    <ToggleGroupItem
      value={it.key}
      aria-label={t(it.label)}
      disabled={readOnly}
      onMouseDown={(e) => e.preventDefault()} // keep the editor's selection
      onClick={() => {
        if (it.key === "link") return;
        it.run(editor);
        editor.tf.focus();
      }}
    >
      <it.Icon />
    </ToggleGroupItem>
  );

  return (
    <div className="flex items-center gap-1">
      <ToggleGroup multiple value={value} size="sm" spacing={0}>
        {items.map((it) => {
          const trigger = <TooltipTrigger render={button(it)} />;
          return (
            <Tooltip key={it.key}>
              {it.key === "link" ? (
                <LinkPopover
                  open={linkOpen}
                  onOpenChange={onLinkOpenChange}
                  trigger={trigger}
                  onApply={applyLink}
                  t={t}
                />
              ) : (
                trigger
              )}
              <TooltipContent>{t(it.label)}</TooltipContent>
            </Tooltip>
          );
        })}
      </ToggleGroup>
      {attach && (
        <>
          <Separator orientation="vertical" className="mx-1 h-5" />
          {attach}
        </>
      )}
    </div>
  );
}
