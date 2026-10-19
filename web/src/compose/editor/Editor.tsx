import { isHotkey, RangeApi, type TRange } from "platejs";
import { createPlatePlugin, Plate, PlateContent, usePlateEditor } from "platejs/react";
import { createContext, type ReactNode, useCallback, useContext, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { T } from "@/shared/i18n";
import { fromHTML, offsetOf, toHTML, toText } from "./html";
import { plugins } from "./plugins";
import { Toolbar } from "./Toolbar";

/** A ⌘K on a selection: the body's plain text (toText) and the selection's [start, end) in it. */
export type CmdKSelection = {
  text: string;
  start: number;
  end: number;
};

// ⌘K's block while its popover shows: the top-level block the selection ends in, whether ⌘K took
// all of it, and the popover's slot.
type Held = { block: number; whole: boolean; slot(el: HTMLDivElement | null): void };
const HeldContext = createContext<Held | null>(null);

/** ⌘K's block: marked as one when ⌘K took all of it; the popover's slot right after it, in the text's flow. */
const HeldBlockPlugin = createPlatePlugin({
  key: "heldBlock",
  render: {
    aboveNodes: ({ path }) => {
      const h = useContext(HeldContext); // aboveNodes may run hooks (Plate's contract)
      if (!h || path.length !== 1 || path[0] !== h.block) return;
      return ({ children }) => (
        <>
          {h.whole ? (
            <div data-held-block className="rounded-sm bg-border ring-4 ring-border">
              {children}
            </div>
          ) : (
            children
          )}
          <div contentEditable={false} ref={h.slot} />
        </>
      );
    },
  },
});

/**
 * The compose body: a Plate editor over the native page's formats, loaded from `initial` (read at
 * mount only) and reporting every content change as HTML. The toolbar, ending in the `attach`
 * slot, renders into `toolbarSlot` (the page's bottom bar; still in the editor's Plate context), else
 * above it. IME compositions are reported so saving can wait for them. ⌘K on a selection goes
 * to `onCmdK` when set (Wit), else opens the link popover. While a `popover` is given (⌘K's), it
 * shows right under the block the selection ends in, and the selection ⌘K took stays marked.
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
  popover?: ReactNode;
  fonts?: string[];
}) {
  const { initial, readOnly, placeholder, onChange, onComposing, t, attach, onCmdK, fonts } = props;
  const { toolbarSlot, popover } = props;
  const held = !!popover;
  const editor = usePlateEditor({
    plugins: [...plugins, HeldBlockPlugin],
    value: (e) => fromHTML(e, initial),
  });
  const [linkOpen, setLinkOpen] = useState(false);
  const taken = useRef<TRange | null>(null); // the selection ⌘K took
  const [mark, setMark] = useState({ block: -1, whole: false });
  const [slot, setSlot] = useState<HTMLDivElement | null>(null);
  // A decoration on the editor root: Slate hands its intersection to every text below.
  const decorate = useCallback(
    ({ entry: [, path] }: { entry: [unknown, number[]] }) =>
      held && taken.current && path.length === 0 ? [{ ...taken.current, held: true }] : [],
    [held],
  );
  const toolbar = (
    <Toolbar
      t={t}
      linkOpen={linkOpen}
      onLinkOpenChange={setLinkOpen}
      attach={attach}
      fonts={fonts}
    />
  );

  return (
    <HeldContext.Provider value={held ? { ...mark, slot: setSlot } : null}>
      <Plate
        editor={editor}
        readOnly={readOnly}
        onValueChange={({ value }) => onChange(toHTML(value))}
      >
        <div className="flex flex-col">
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
            onDOMBeforeInput={(e: Event) => {
              const inputEvent = e as InputEvent;
              if (inputEvent.inputType === "insertReplacementText") {
                const text = inputEvent.data || inputEvent.dataTransfer?.getData("text/plain");
                if (text) {
                  inputEvent.preventDefault();
                  if (readOnly) return;
                  let targetRange: TRange | null = null;
                  const ranges = inputEvent.getTargetRanges?.();
                  if (ranges && ranges.length > 0) {
                    try {
                      targetRange = editor.api.toSlateRange(ranges[0]);
                    } catch {}
                  }
                  if (!targetRange) {
                    targetRange = editor.selection;
                  }
                  if (targetRange) {
                    const [leaf] = editor.api.leaf(targetRange.anchor) ?? [];
                    const marks: Record<string, unknown> = {};
                    if (leaf) {
                      for (const [k, v] of Object.entries(leaf)) {
                        if (k !== "text") marks[k] = v;
                      }
                    }
                    editor.tf.select(targetRange);
                    for (const [k, v] of Object.entries(marks)) {
                      editor.tf.addMark(k, v);
                    }
                    editor.tf.insertText(text);
                  }
                }
              }
            }}
            onBeforeInput={(e: React.FormEvent<HTMLDivElement> & { nativeEvent: InputEvent }) => {
              const inputEvent = e.nativeEvent;
              if (inputEvent.inputType === "insertReplacementText") {
                const text = inputEvent.data || inputEvent.dataTransfer?.getData("text/plain");
                if (text) {
                  e.preventDefault();
                  if (readOnly) return;
                  let targetRange: TRange | null = null;
                  const ranges = inputEvent.getTargetRanges?.();
                  if (ranges && ranges.length > 0) {
                    try {
                      targetRange = editor.api.toSlateRange(ranges[0]);
                    } catch {}
                  }
                  if (!targetRange) {
                    targetRange = editor.selection;
                  }
                  if (targetRange) {
                    const [leaf] = editor.api.leaf(targetRange.anchor) ?? [];
                    const marks: Record<string, unknown> = {};
                    if (leaf) {
                      for (const [k, v] of Object.entries(leaf)) {
                        if (k !== "text") marks[k] = v;
                      }
                    }
                    editor.tf.select(targetRange);
                    for (const [k, v] of Object.entries(marks)) {
                      editor.tf.addMark(k, v);
                    }
                    editor.tf.insertText(text);
                  }
                }
              }
            }}
            onKeyDown={(e) => {
              if (
                e.key === "Enter" &&
                e.shiftKey &&
                !e.nativeEvent.isComposing &&
                e.target === e.currentTarget
              ) {
                e.preventDefault();
                if (readOnly) return;
                editor.tf.insertText("\n");
                return;
              }
              // ⌘K on a selection: Wit's rewrite when offered, else the link popover. Not from
              // inside the popover, which sits in the editable's DOM.
              if (
                e.target !== e.currentTarget ||
                e.nativeEvent.isComposing ||
                !isHotkey("mod+k", { byKey: true }, e.nativeEvent)
              )
                return;
              e.preventDefault();
              if (readOnly) return;
              // Wit only for a non-empty selection; anything else is the link popover.
              if (!onCmdK || !editor.selection || !editor.api.isExpanded())
                return setLinkOpen(true);
              taken.current = editor.selection;
              const [a, b] = RangeApi.edges(editor.selection);
              const top = b.path[0];
              setMark({
                block: top,
                whole:
                  a.path[0] === top && editor.api.isStart(a, [top]) && editor.api.isEnd(b, [top]),
              });
              const v = editor.children;
              onCmdK({ text: toText(v), start: offsetOf(v, a), end: offsetOf(v, b) });
            }}
            className="text-lg leading-snug outline-none [&>*+*]:mt-3 [&_a]:text-primary [&_a]:underline [&_blockquote]:border-l-2 [&_blockquote]:pl-3 [&_blockquote]:text-muted-foreground [&_ol]:list-decimal [&_ol]:pl-6 [&_ul]:list-disc [&_ul]:pl-6"
          />
        </div>
        {popover && slot && createPortal(popover, slot)}
      </Plate>
    </HeldContext.Provider>
  );
}
