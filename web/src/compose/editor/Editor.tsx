import { isHotkey } from "platejs";
import { Plate, PlateContent, usePlateEditor } from "platejs/react";
import { type ReactNode, useState } from "react";
import { fromHTML, toHTML } from "./html";
import { plugins } from "./plugins";
import { type T, Toolbar } from "./Toolbar";

/**
 * The compose body: a Plate editor over the native page's formats, loaded from `initial` (read at
 * mount only) and reporting every content change as HTML. The toolbar sits above it, ending in the
 * `attach` slot. IME compositions are reported so saving can wait for them.
 */
export function Editor(props: {
  initial: string;
  readOnly: boolean;
  placeholder: string;
  onChange(html: string): void;
  onComposing(on: boolean): void;
  t: T;
  attach?: ReactNode;
}) {
  const { initial, readOnly, placeholder, onChange, onComposing, t, attach } = props;
  const editor = usePlateEditor({ plugins, value: (e) => fromHTML(e, initial) });
  const [linkOpen, setLinkOpen] = useState(false);

  return (
    <Plate
      editor={editor}
      readOnly={readOnly}
      onValueChange={({ value }) => onChange(toHTML(value))}
    >
      <div className="flex min-h-0 flex-1 flex-col gap-2">
        <Toolbar t={t} linkOpen={linkOpen} onLinkOpenChange={setLinkOpen} attach={attach} />
        <PlateContent
          aria-label={t("compose.body")}
          placeholder={placeholder}
          onCompositionStart={() => onComposing(true)}
          onCompositionEnd={() => onComposing(false)}
          onKeyDown={(e) => {
            // ⌘K: the link popover, for a selection (plan 3 routes it to Wit when a model is set).
            if (!isHotkey("mod+k", { byKey: true }, e.nativeEvent)) return;
            e.preventDefault();
            if (!readOnly && editor.api.isExpanded()) setLinkOpen(true);
          }}
          className="min-h-40 flex-1 overflow-auto rounded-md border px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50 [&_a]:text-primary [&_a]:underline [&_blockquote]:border-l-2 [&_blockquote]:pl-3 [&_blockquote]:text-muted-foreground [&_ol]:list-decimal [&_ol]:pl-6 [&_ul]:list-disc [&_ul]:pl-6"
        />
      </div>
    </Plate>
  );
}
