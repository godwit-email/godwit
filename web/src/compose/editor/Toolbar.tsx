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
import {
  Plate,
  useEditorReadOnly,
  useEditorRef,
  useEditorSelector,
  usePlateEditor,
} from "platejs/react";
import { type ReactElement, type ReactNode, useEffect, useRef } from "react";
import { Separator } from "@/components/ui/separator";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { T } from "@/shared/i18n";
import { safeHref } from "./html";
import { LinkPopover } from "./LinkPopover";

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
    run: (e) => e.tf.toggleBlock("blockquote", { wrap: true }), // BlockquotePlugin's own toggle
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
  // A frozen editor (an operation running) closes the dialog; upsertLink would ignore readOnly.
  useEffect(() => {
    if (readOnly && linkOpen) onLinkOpenChange(false);
  }, [readOnly, linkOpen, onLinkOpenChange]);

  const applyLink = (url: string) => {
    if (readOnly) return true; // closes, applies nothing
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
      // read-only (Wit's lock), the toggles do nothing but keep their look, as the design shows
      className="size-7 min-w-7 rounded-md px-0 text-foreground/80 disabled:opacity-100"
      disabled={readOnly}
      onMouseDown={(e) => e.preventDefault()} // keep the editor's selection
      onClick={() => {
        if (it.key === "link") return;
        it.run(editor);
        editor.tf.focus();
      }}
    >
      <it.Icon className="size-3.75" />
    </ToggleGroupItem>
  );

  return (
    <div className="-ml-1.5 flex flex-wrap items-center gap-0.5">
      <ToggleGroup multiple value={value} size="sm" spacing={0.5}>
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
          <Separator
            orientation="vertical"
            className="mx-1.5 h-4.5 bg-transparent data-vertical:w-0 data-vertical:self-center data-vertical:border-l-[0.5px]"
          />
          {attach}
        </>
      )}
    </div>
  );
}

/** The toolbar while no editor shows (a review or Wit writing in its place): every toggle off. */
export function IdleToolbar({ t, attach }: { t: T; attach?: ReactNode }) {
  const editor = usePlateEditor({ plugins: [] });
  return (
    <Plate editor={editor} readOnly>
      <Toolbar t={t} linkOpen={false} onLinkOpenChange={() => {}} attach={attach} />
    </Plate>
  );
}
