import { upsertLink } from "@platejs/link";
import { toggleBulletedList, toggleNumberedList } from "@platejs/list-classic";
import {
  type RemixiconComponentType,
  RiBold,
  RiCheckLine,
  RiDoubleQuotesL,
  RiFontFamily,
  RiFontSize,
  RiItalic,
  RiLink,
  RiListOrdered,
  RiListUnordered,
  RiStrikethrough,
  RiUnderline,
} from "@remixicon/react";
import type { SlateEditor, TRange } from "platejs";
import {
  Plate,
  useEditorReadOnly,
  useEditorRef,
  useEditorSelector,
  usePlateEditor,
} from "platejs/react";
import { type ReactElement, type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Separator } from "@/components/ui/separator";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { T } from "@/shared/i18n";
import { safeHref } from "./html";
import { LinkPopover } from "./LinkPopover";

type Key = "bold" | "italic" | "underline" | "strikethrough" | "link" | "ul" | "ol" | "blockquote";

const items: {
  key: Key;
  label: string;
  Icon: RemixiconComponentType;
  run(e: SlateEditor): void;
}[] = [
  { key: "bold", label: "compose.bold", Icon: RiBold, run: (e) => e.tf.toggleMark("bold") },
  { key: "italic", label: "compose.italic", Icon: RiItalic, run: (e) => e.tf.toggleMark("italic") },
  {
    key: "underline",
    label: "compose.underline",
    Icon: RiUnderline,
    run: (e) => e.tf.toggleMark("underline"),
  },
  {
    key: "strikethrough",
    label: "compose.strikethrough",
    Icon: RiStrikethrough,
    run: (e) => e.tf.toggleMark("strikethrough"),
  },
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

const fontSizes = [
  { label: "Standard (14 pt)", value: "" },
  { label: "9 pt", value: "9pt" },
  { label: "10 pt", value: "10pt" },
  { label: "11 pt", value: "11pt" },
  { label: "12 pt", value: "12pt" },
  { label: "13 pt", value: "13pt" },
  { label: "14 pt", value: "14pt" },
  { label: "18 pt", value: "18pt" },
  { label: "24 pt", value: "24pt" },
  { label: "36 pt", value: "36pt" },
  { label: "48 pt", value: "48pt" },
  { label: "64 pt", value: "64pt" },
  { label: "72 pt", value: "72pt" },
];

const standardFonts = [
  { label: "Standard (Systemschrift)", value: "" },
  { label: "American Typewriter", value: "'American Typewriter', serif" },
  { label: "Arial", value: "Arial, sans-serif" },
  { label: "Avenir Next", value: "'Avenir Next', Avenir, sans-serif" },
  { label: "Baskerville", value: "Baskerville, serif" },
  { label: "Charter", value: "Charter, serif" },
  { label: "Courier New", value: "'Courier New', Courier, monospace" },
  { label: "Futura", value: "Futura, sans-serif" },
  { label: "Georgia", value: "Georgia, serif" },
  { label: "Gill Sans", value: "'Gill Sans', sans-serif" },
  { label: "Helvetica Neue", value: "'Helvetica Neue', Helvetica, Arial, sans-serif" },
  { label: "Hoefler Text", value: "'Hoefler Text', serif" },
  { label: "Menlo", value: "Menlo, Monaco, monospace" },
  { label: "Monaco", value: "Monaco, monospace" },
  { label: "Optima", value: "Optima, sans-serif" },
  { label: "Palatino", value: "Palatino, 'Palatino Linotype', serif" },
  { label: "SF Pro", value: "-apple-system, BlinkMacSystemFont, sans-serif" },
  { label: "Times New Roman", value: "'Times New Roman', Times, serif" },
  { label: "Trebuchet MS", value: "'Trebuchet MS', sans-serif" },
  { label: "Verdana", value: "Verdana, sans-serif" },
];

// The pressed formats at the selection, space-separated (a string keeps the selector's equality cheap).
function active(e: SlateEditor): string {
  if (!e.selection) return "";
  const marks = e.api.marks() ?? {};
  const list = e.api.above({ match: { type: ["ul", "ol"] } })?.[0].type;
  return [
    marks.bold && "bold",
    marks.italic && "italic",
    marks.underline && "underline",
    marks.strikethrough && "strikethrough",
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
  fonts?: string[];
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

  const currentFontSize = useEditorSelector(
    (e) => (e.selection ? ((e.api.marks()?.fontSize as string) ?? "") : ""),
    [],
  );
  const currentFontFamily = useEditorSelector(
    (e) => (e.selection ? ((e.api.marks()?.fontFamily as string) ?? "") : ""),
    [],
  );

  const [fontOpen, setFontOpen] = useState(false);
  const [sizeOpen, setSizeOpen] = useState(false);

  const applyFontFamily = (val: string) => {
    if (saved.current) editor.tf.select(saved.current);
    if (!val) {
      editor.tf.removeMark("fontFamily");
    } else {
      editor.tf.addMark("fontFamily", val);
    }
    editor.tf.focus();
    setTimeout(() => {
      if (saved.current) editor.tf.select(saved.current);
      if (!val) {
        editor.tf.removeMark("fontFamily");
      } else {
        editor.tf.addMark("fontFamily", val);
      }
      editor.tf.focus();
    }, 10);
  };

  const applyFontSize = (val: string) => {
    if (saved.current) editor.tf.select(saved.current);
    if (!val) {
      editor.tf.removeMark("fontSize");
    } else {
      editor.tf.addMark("fontSize", val);
    }
    editor.tf.focus();
    setTimeout(() => {
      if (saved.current) editor.tf.select(saved.current);
      if (!val) {
        editor.tf.removeMark("fontSize");
      } else {
        editor.tf.addMark("fontSize", val);
      }
      editor.tf.focus();
    }, 10);
  };

  const fonts: { label: string; value: string }[] = useMemo(() => {
    if (props.fonts && props.fonts.length > 0) {
      return [
        { label: "Standard (Systemschrift)", value: "" },
        ...props.fonts.map((name) => ({ label: name, value: `'${name}', sans-serif` })),
      ];
    }
    return standardFonts;
  }, [props.fonts]);

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
      <Separator
        orientation="vertical"
        className="mx-0.5 h-4.5 bg-transparent data-vertical:w-0 data-vertical:self-center data-vertical:border-l-[0.5px]"
      />
      <DropdownMenu
        modal={false}
        highlightItemOnHover={false}
        open={fontOpen}
        onOpenChange={(open) => {
          if (open) {
            saved.current = editor.selection;
          } else {
            setTimeout(() => {
              if (saved.current) editor.tf.select(saved.current);
              editor.tf.focus();
            }, 0);
          }
          setFontOpen(open);
        }}
      >
        <Tooltip>
          <TooltipTrigger
            render={
              <DropdownMenuTrigger
                aria-label={t("compose.font_family")}
                disabled={readOnly}
                onMouseDown={(e) => {
                  saved.current = editor.selection;
                  e.preventDefault();
                }}
                className="inline-flex size-7 items-center justify-center rounded-md text-foreground/80 hover:bg-accent hover:text-accent-foreground disabled:opacity-50"
              >
                <RiFontFamily className="size-3.75" />
              </DropdownMenuTrigger>
            }
          />
          <TooltipContent>{t("compose.font_family")}</TooltipContent>
        </Tooltip>
        <DropdownMenuContent
          align="start"
          initialFocus={false}
          className="w-auto min-w-56 max-h-60 overflow-y-auto"
          onMouseDown={(e) => e.preventDefault()}
        >
          {fonts.map((f) => {
            const isSelected = currentFontFamily === f.value;
            return (
              <DropdownMenuItem
                key={f.label}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => applyFontFamily(f.value)}
                style={{ fontFamily: f.value || "inherit" }}
                className="flex items-center justify-between gap-3"
              >
                <span className="whitespace-nowrap">{f.label}</span>
                {isSelected && <RiCheckLine className="ml-2 size-3.5 shrink-0 text-primary" />}
              </DropdownMenuItem>
            );
          })}
        </DropdownMenuContent>
      </DropdownMenu>
      <DropdownMenu
        modal={false}
        highlightItemOnHover={false}
        open={sizeOpen}
        onOpenChange={(open) => {
          if (open) {
            saved.current = editor.selection;
          } else {
            setTimeout(() => {
              if (saved.current) editor.tf.select(saved.current);
              editor.tf.focus();
            }, 0);
          }
          setSizeOpen(open);
        }}
      >
        <Tooltip>
          <TooltipTrigger
            render={
              <DropdownMenuTrigger
                aria-label={t("compose.font_size")}
                disabled={readOnly}
                onMouseDown={(e) => {
                  saved.current = editor.selection;
                  e.preventDefault();
                }}
                className="inline-flex size-7 items-center justify-center rounded-md text-foreground/80 hover:bg-accent hover:text-accent-foreground disabled:opacity-50"
              >
                <RiFontSize className="size-3.75" />
              </DropdownMenuTrigger>
            }
          />
          <TooltipContent>{t("compose.font_size")}</TooltipContent>
        </Tooltip>
        <DropdownMenuContent
          align="start"
          initialFocus={false}
          className="w-auto min-w-44 max-h-60 overflow-y-auto"
          onMouseDown={(e) => e.preventDefault()}
        >
          {fontSizes.map((s) => {
            const isSelected = currentFontSize === s.value;
            return (
              <DropdownMenuItem
                key={s.label}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => applyFontSize(s.value)}
                className="flex items-center justify-between gap-3"
              >
                <span className="whitespace-nowrap">{s.label}</span>
                {isSelected && <RiCheckLine className="ml-2 size-3.5 shrink-0 text-primary" />}
              </DropdownMenuItem>
            );
          })}
        </DropdownMenuContent>
      </DropdownMenu>
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
export function IdleToolbar({ t, attach, fonts }: { t: T; attach?: ReactNode; fonts?: string[] }) {
  const editor = usePlateEditor({ plugins: [] });
  return (
    <Plate editor={editor} readOnly>
      <Toolbar t={t} linkOpen={false} onLinkOpenChange={() => {}} attach={attach} fonts={fonts} />
    </Plate>
  );
}
