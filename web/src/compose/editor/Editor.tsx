import { isHotkey, RangeApi, type TRange } from "platejs";
import { Plate, PlateContent, usePlateEditor } from "platejs/react";
import { type ReactNode, useCallback, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { T } from "@/shared/i18n";
import { fromHTML, offsetOf, toHTML, toText } from "./html";
import { plugins } from "./plugins";
import { Toolbar } from "./Toolbar";

/** A ⌘K on a selection: the body's plain text (toText), the selection's [start, end) in it, and where the selection ends on screen. */
export type CmdKSelection = {
  text: string;
  start: number;
  end: number;
  at: { left: number; top: number };
};

// Below the selection, in the viewport (jsdom has no layout: the origin).
function below(): { left: number; top: number } {
  const r = window.getSelection()?.getRangeAt(0).getBoundingClientRect?.();
  return r ? { left: r.left, top: r.bottom + 4 } : { left: 0, top: 0 };
}

/**
 * The compose body: a Plate editor over the native page's formats, loaded from `initial` (read at
 * mount only) and reporting every content change as HTML. The toolbar, ending in the `attach`
 * slot, renders into `toolbarSlot` (the page's bottom bar; still in the editor's Plate context), else
 * above it. While `held` (⌘K's popover has focus), the selection ⌘K took stays marked. IME compositions are reported so saving can wait for them. ⌘K on a selection goes
 * to `onCmdK` when set (Wit), else opens the link popover.
 */
export function Editor(props: {
  initial: string;
  readOnly: boolean;
  placeholder: string;
  onChange(html: string): void;
  onComposing(on: boolean): void;
  t: T;
  attach?: ReactNode;
  onCmdK?(s: CmdKSelection): void;
  toolbarSlot?: HTMLElement | null;
  held?: boolean;
}) {
  const { initial, readOnly, placeholder, onChange, onComposing, t, attach, onCmdK } = props;
  const { toolbarSlot, held } = props;
  const editor = usePlateEditor({ plugins, value: (e) => fromHTML(e, initial) });
  const [linkOpen, setLinkOpen] = useState(false);
  const taken = useRef<TRange | null>(null); // the selection ⌘K took
  // A decoration on the editor root: Slate hands its intersection to every text below.
  const decorate = useCallback(
    ({ entry: [, path] }: { entry: [unknown, number[]] }) =>
      held && taken.current && path.length === 0 ? [{ ...taken.current, held: true }] : [],
    [held],
  );
  const toolbar = (
    <Toolbar t={t} linkOpen={linkOpen} onLinkOpenChange={setLinkOpen} attach={attach} />
  );

  return (
    <Plate
      editor={editor}
      readOnly={readOnly}
      onValueChange={({ value }) => onChange(toHTML(value))}
    >
      <div className="flex flex-1 flex-col">
        {toolbarSlot ? createPortal(toolbar, toolbarSlot) : toolbar}
        {/* No overflow on the editable itself: WebKit (WKWebView) stops repainting a contenteditable
            that is also a scroll container — typed text appeared only after a relayout. The page's
            wrapper around the fields and the body scrolls instead. */}
        <PlateContent
          aria-label={t("compose.body")}
          placeholder={placeholder}
          decorate={decorate}
          onCompositionStart={() => onComposing(true)}
          onCompositionEnd={() => onComposing(false)}
          onKeyDown={(e) => {
            // ⌘K on a selection: Wit's rewrite when offered, else the link popover.
            if (e.nativeEvent.isComposing || !isHotkey("mod+k", { byKey: true }, e.nativeEvent))
              return;
            e.preventDefault();
            if (readOnly) return;
            // Wit only for a non-empty selection; anything else is the link popover.
            if (!onCmdK || !editor.selection || !editor.api.isExpanded()) return setLinkOpen(true);
            taken.current = editor.selection;
            const [a, b] = RangeApi.edges(editor.selection);
            const v = editor.children;
            onCmdK({ text: toText(v), start: offsetOf(v, a), end: offsetOf(v, b), at: below() });
          }}
          className="min-h-70 flex-1 p-4 text-lg leading-[1.75] outline-none [&>*+*]:mt-3 [&_a]:text-primary [&_a]:underline [&_blockquote]:border-l-2 [&_blockquote]:pl-3 [&_blockquote]:text-muted-foreground [&_ol]:list-decimal [&_ol]:pl-6 [&_ul]:list-disc [&_ul]:pl-6"
        />
      </div>
    </Plate>
  );
}
