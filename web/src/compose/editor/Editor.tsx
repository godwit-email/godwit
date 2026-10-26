import { isHotkey, RangeApi, type TRange } from "platejs";
import { createPlatePlugin, Plate, PlateContent, usePlateEditor } from "platejs/react";
import {
  createContext,
  type ReactNode,
  type Ref,
  useCallback,
  useContext,
  useEffect,
  useImperativeHandle,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import type { T } from "@/shared/i18n";
import { deserialize, fromHTML, offsetOf, toHTML, toText } from "./html";
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

/** The page's hold on a mounted editor: `setValue` replaces the body as one undoable step, unreported. */
export type EditorHandle = { setValue(html: string): void };

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
  ref?: Ref<EditorHandle>;
}) {
  const { initial, readOnly, placeholder, onChange, onComposing, t, attach, onCmdK } = props;
  const { toolbarSlot, popover, ref } = props;
  const held = !!popover;
  const editor = usePlateEditor({
    plugins: [...plugins, HeldBlockPlugin],
    value: (e) => fromHTML(e, initial),
  });
  const quiet = useRef(false); // the change setValue makes is the page's own, not an edit
  useImperativeHandle(
    ref,
    () => ({
      setValue(html) {
        quiet.current = true;
        editor.tf.withNewBatch(() => editor.tf.setValue(deserialize(editor, html)));
      },
    }),
    [editor],
  );
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
    <Toolbar t={t} linkOpen={linkOpen} onLinkOpenChange={setLinkOpen} attach={attach} />
  );

  const editableRef = useRef<HTMLDivElement | null>(null);
  const lastReplacementTime = useRef(0);

  const handleReplacementText = useCallback(
    (inputEvent: InputEvent) => {
      if (inputEvent.inputType !== "insertReplacementText") return false;
      const text = inputEvent.data || inputEvent.dataTransfer?.getData("text/plain");
      if (!text) return false;

      inputEvent.preventDefault();
      if ("stopImmediatePropagation" in inputEvent) {
        inputEvent.stopImmediatePropagation();
      }
      if (readOnly) return true;
      lastReplacementTime.current = Date.now();

      let targetRange: TRange | null = null;
      const ranges = inputEvent.getTargetRanges?.();
      if (ranges && ranges.length > 0) {
        try {
          const sr = ranges[0];
          const domRange = document.createRange();
          domRange.setStart(sr.startContainer, sr.startOffset);
          domRange.setEnd(sr.endContainer, sr.endOffset);
          targetRange = editor.api.toSlateRange(domRange);
        } catch {}
      }
      if (!targetRange || RangeApi.isCollapsed(targetRange)) {
        const sel = editor.selection;
        if (sel && !RangeApi.isCollapsed(sel)) {
          targetRange = sel;
        } else if (sel) {
          // The word before the caret, without the space typed after it.
          const head = sel.anchor;
          const before = editor.api.before(head, { unit: "word" }) ?? editor.api.start(head.path);
          const word = editor.api.string({ anchor: before, focus: head });
          const trail = word.length - word.trimEnd().length;
          const end = trail ? (editor.api.before(head, { distance: trail }) ?? head) : head;
          targetRange = { anchor: before, focus: end };
        }
      }
      if (targetRange) {
        const [start] = RangeApi.edges(targetRange);
        const [leaf] = editor.api.leaf(start) ?? [];
        let marks: Record<string, unknown> = {};
        if (leaf) {
          for (const [k, v] of Object.entries(leaf)) {
            if (k !== "text") marks[k] = v;
          }
        }
        const activeMarks = editor.api.marks();
        if (activeMarks) {
          marks = { ...marks, ...activeMarks };
        }
        editor.tf.select(targetRange);
        editor.tf.delete();
        for (const [k, v] of Object.entries(marks)) {
          editor.tf.addMark(k, v);
        }
        editor.tf.insertText(text);
      }
      return true;
    },
    [editor, readOnly],
  );

  useEffect(() => {
    const el = editableRef.current;
    if (!el) return;
    const onBeforeInput = (e: Event) => {
      handleReplacementText(e as InputEvent);
    };
    el.addEventListener("beforeinput", onBeforeInput, { capture: true });
    return () => {
      el.removeEventListener("beforeinput", onBeforeInput, { capture: true });
    };
  }, [handleReplacementText]);

  return (
    <HeldContext.Provider value={held ? { ...mark, slot: setSlot } : null}>
      <Plate
        editor={editor}
        readOnly={readOnly}
        onValueChange={({ value }) => {
          if (quiet.current) quiet.current = false;
          else onChange(toHTML(value));
        }}
      >
        <div className="flex flex-col">
          {toolbarSlot ? createPortal(toolbar, toolbarSlot) : toolbar}
          {/* No overflow on the editable itself: WebKit (WKWebView) stops repainting a contenteditable
            that is also a scroll container — typed text appeared only after a relayout. The page's
            wrapper around the fields and the body scrolls instead. */}
          <PlateContent
            ref={editableRef}
            aria-label={t("compose.body")}
            placeholder={placeholder}
            decorate={decorate}
            onCompositionStart={() => onComposing(true)}
            onCompositionEnd={() => onComposing(false)}
            onKeyDown={(e) => {
              if (
                e.key === "Enter" &&
                !e.shiftKey &&
                !e.nativeEvent.isComposing &&
                Date.now() - lastReplacementTime.current < 250
              ) {
                lastReplacementTime.current = 0; // swallow the one Enter that accepted it, no more
                e.preventDefault();
                e.stopPropagation();
                return;
              }
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
