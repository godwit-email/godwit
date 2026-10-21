import { BoldPlugin, ItalicPlugin, UnderlinePlugin } from "@platejs/basic-nodes/react";
import { upsertLink } from "@platejs/link";
import {
  type RemixiconComponentType,
  RiBold,
  RiCodeSSlashLine,
  RiImageLine,
  RiItalic,
  RiLink,
  RiUnderline,
} from "@remixicon/react";
import type { SlateEditor, TRange, Value } from "platejs";
import {
  createPlatePlugin,
  Plate,
  PlateContent,
  PlateElement,
  type PlateElementProps,
  useEditorRef,
  useEditorSelector,
  usePlateEditor,
} from "platejs/react";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { fromHTML, IMAGE_SRC, safeHref, toHTML, toText } from "@/compose/editor/html";
import { linkPlugin, SafePastePlugin } from "@/compose/editor/plugins";
import { SignaturePage as api, events, type SignatureState } from "@/mygo";
import { type T, useT } from "@/shared/i18n";
import { applyTheme, type Theme as PageTheme } from "@/shared/theme";
import { type Doc, type Refusal, signatureSaver } from "./saver";

/** At most this many images (Go's core.SignatureImagesMax; Save refuses more). */
const MAX_IMAGES = 4;

/** An image block (`url` a data: URL): inserted with 插入图片 or loaded; pasted ones are dropped. */
function ImageElement(props: PlateElementProps) {
  return (
    <PlateElement {...props}>
      <img
        src={String(props.element.url)}
        alt=""
        contentEditable={false}
        className="max-w-full select-none"
      />
      {props.children}
    </PlateElement>
  );
}

const ImagePlugin = createPlatePlugin({
  key: "img",
  node: { isElement: true, isVoid: true, component: ImageElement },
  parsers: {
    html: {
      deserializer: {
        rules: [{ validNodeName: "IMG" }],
        parse: ({ element }) => {
          const url = element.getAttribute("src") ?? "";
          return IMAGE_SRC.test(url) ? { type: "img", url } : undefined;
        },
      },
    },
  },
});

// The signature's formats: bold, italic, underline, link, image. No lists.
const plugins = [
  BoldPlugin,
  ItalicPlugin,
  UnderlinePlugin,
  linkPlugin,
  ImagePlugin,
  SafePastePlugin,
];

const images = (v: Value) => v.filter((n) => n.type === "img").length;

type Mark = "bold" | "italic" | "underline";
const marks: { key: Mark; label: string; Icon: RemixiconComponentType }[] = [
  { key: "bold", label: "compose.bold", Icon: RiBold },
  { key: "italic", label: "compose.italic", Icon: RiItalic },
  { key: "underline", label: "compose.underline", Icon: RiUnderline },
];

function active(e: SlateEditor): string {
  if (!e.selection) return "";
  const m = e.api.marks() ?? {};
  return [...marks.map((k) => m[k.key] && k.key), e.api.some({ match: { type: "a" } }) && "link"]
    .filter(Boolean)
    .join(" ");
}

const button = "size-7 min-w-7 rounded-md px-0 text-muted-foreground";

const item = (
  t: T,
  key: string,
  label: string,
  Icon: RemixiconComponentType,
  run?: () => void,
  disabled = false,
) => (
  <ToggleGroupItem
    key={key}
    value={key}
    disabled={disabled}
    aria-label={t(label)}
    title={t(label)}
    className={button}
    onMouseDown={(e) => e.preventDefault()} // keep the editor's selection
    onClick={run}
  >
    <Icon className="size-4" />
  </ToggleGroupItem>
);

/** The toolbar's other side: a refused save or image, then the HTML toggle (native edits the code). */
type Mode = { alert?: string; onHTML(): void };

/** The toolbar row: formats, what Go refused, and 「</> HTML」 at the end. */
function Bar({ t, mode, children }: { t: T; mode: Mode; children?: ReactNode }) {
  return (
    <div
      data-slot="signature-toolbar"
      className="flex items-center border-b-[0.5px] bg-sidebar px-1.5 py-1"
    >
      {children}
      {mode.alert && (
        <span role="alert" className="ml-2 min-w-0 truncate text-xs text-destructive">
          {t(mode.alert)}
        </span>
      )}
      <span className="flex-1" />
      <Button
        variant="ghost"
        size="sm"
        aria-pressed={false}
        aria-label={t("settings.account.signature.html.label")}
        title={t("settings.account.signature.html.label")}
        className="h-7 gap-1.5 px-2 text-muted-foreground text-xs"
        onMouseDown={(e) => e.preventDefault()}
        onClick={mode.onHTML}
      >
        <RiCodeSSlashLine className="size-4" />
        {t("settings.account.signature.html")}
      </Button>
    </div>
  );
}

/** The formats, greyed out: a layout signature edits only its text. */
function Disabled({ t }: { t: T }) {
  return (
    <ToggleGroup multiple value={[]} size="sm" spacing={0.5}>
      {marks.map((m) => item(t, m.key, m.label, m.Icon, undefined, true))}
      <span className="mx-1 h-4 border-l-[0.5px]" />
      {item(t, "link", "compose.link", RiLink, undefined, true)}
      {item(t, "image", "settings.account.signature.image", RiImageLine, undefined, true)}
    </ToggleGroup>
  );
}

/**
 * 加粗, 斜体, 下划线 | 链接, 插入图片; the link's address is asked inline (the page is too short for a
 * popover). An image Go refused is said beside the buttons.
 */
function Toolbar({ t, mode, pickImage }: { t: T; mode: Mode; pickImage(): Promise<string> }) {
  const editor = useEditorRef();
  const value = useEditorSelector(active, []).split(" ").filter(Boolean);
  const full = useEditorSelector((e) => images(e.children) >= MAX_IMAGES, []);
  const [link, setLink] = useState<string>();
  const [bad, setBad] = useState(false);
  const [refused, setRefused] = useState(false);
  const saved = useRef<TRange | null>(null);

  if (link !== undefined)
    return (
      <form
        className="flex items-center gap-1 border-b-[0.5px] bg-sidebar px-1.5 py-1"
        onSubmit={(e) => {
          e.preventDefault();
          const href = safeHref(link);
          if (!href) return setBad(true);
          if (saved.current) editor.tf.select(saved.current);
          upsertLink(editor, { url: href });
          setLink(undefined);
          editor.tf.focus();
        }}
      >
        <Input
          autoFocus
          aria-label={t("compose.link.url")}
          aria-invalid={bad}
          placeholder="https://"
          value={link}
          className="h-7"
          onChange={(e) => {
            setLink(e.target.value);
            setBad(false);
          }}
          onKeyDown={(e) => e.key === "Escape" && setLink(undefined)}
        />
        <Button type="submit" size="sm">
          {t("compose.link.apply")}
        </Button>
      </form>
    );

  // The row spans the card; ToggleGroup itself is fit-to-content and rounded.
  return (
    <Bar
      t={t}
      mode={{ ...mode, alert: refused ? "settings.account.signature.image_invalid" : mode.alert }}
    >
      <ToggleGroup multiple value={value} size="sm" spacing={0.5}>
        {marks.map((m) =>
          item(t, m.key, m.label, m.Icon, () => {
            editor.tf.toggleMark(m.key);
            editor.tf.focus();
          }),
        )}
        <span className="mx-1 h-4 border-l-[0.5px]" />
        {item(t, "link", "compose.link", RiLink, () => {
          saved.current = editor.selection;
          setBad(false);
          setLink("");
        })}
        {item(
          t,
          "image",
          "settings.account.signature.image",
          RiImageLine,
          () => {
            setRefused(false);
            const at = editor.selection;
            pickImage().then(
              (url) => {
                if (!url || images(editor.children) >= MAX_IMAGES) return;
                editor.tf.insertNodes(
                  { type: "img", url, children: [{ text: "" }] },
                  at ? { at, select: true } : { at: [editor.children.length], select: true },
                );
                editor.tf.focus();
              },
              () => setRefused(true),
            );
          },
          full,
        )}
      </ToggleGroup>
    </Bar>
  );
}

/**
 * Whether html (sanitized by Go) holds what the editor's formats cannot keep: a table, or an
 * attribute other than <a href> and <img src> (a style, a color, a width…).
 */
export function hasLayout(html: string): boolean {
  const body = new DOMParser().parseFromString(html, "text/html").body;
  if (body.querySelector("table")) return true;
  return [...body.querySelectorAll("*")].some((el) =>
    [...el.attributes].some(
      (a) =>
        !((el.tagName === "A" && a.name === "href") || (el.tagName === "IMG" && a.name === "src")),
    ),
  );
}

/** Inserts node at the selection inside box and puts the caret after it. */
function insertAtCaret(box: HTMLElement, node: Node) {
  const sel = window.getSelection();
  if (!sel?.rangeCount || !box.contains(sel.anchorNode)) return;
  const r = sel.getRangeAt(0);
  r.deleteContents();
  r.insertNode(node);
  r.setStartAfter(node);
  r.collapse(true);
  sel.removeAllRanges();
  sel.addRange(r);
}

/**
 * A layout signature in the rich mode: Go's sanitized HTML (never anything else) shown as is, its
 * text editable. A paste is plain text, Enter a <br>; the formats are off, and the HTML mode
 * changes the layout.
 */
function LayoutEditor(props: { t: T; initial: string; mode: Mode; onChange(html: string): void }) {
  const { t, initial, mode, onChange } = props;
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (box.current) box.current.innerHTML = initial; // Go sanitized it (SignaturePage.Load)
  }, [initial]);
  const changed = () => {
    const el = box.current;
    if (el) onChange(el.textContent?.trim() || el.querySelector("img") ? el.innerHTML : "");
  };
  return (
    <div className="flex h-screen flex-col">
      <Bar t={t} mode={mode}>
        <Disabled t={t} />
      </Bar>
      <div className="flex items-center gap-2 border-b-[0.5px] px-3 py-2 text-xs text-muted-foreground">
        <span className="flex-1">{t("settings.account.signature.layout")}</span>
        <Button variant="outline" size="sm" className="h-7 text-xs" onClick={mode.onHTML}>
          {t("settings.account.signature.edit_html")}
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-auto">
        {/* biome-ignore lint/a11y/useSemanticElements: rich text (a table, images) cannot be a <textarea> */}
        <div
          ref={box}
          role="textbox"
          aria-multiline="true"
          tabIndex={0}
          aria-label={t("settings.account.signature.label")}
          contentEditable
          suppressContentEditableWarning
          className="min-h-full px-3 py-3 leading-normal outline-none"
          onInput={changed}
          onPaste={(e) => {
            e.preventDefault();
            if (!box.current) return;
            insertAtCaret(
              box.current,
              document.createTextNode(e.clipboardData.getData("text/plain")),
            );
            changed();
          }}
          onDrop={(e) => e.preventDefault()}
          onKeyDown={(e) => {
            if (e.key !== "Enter" || !box.current) return;
            e.preventDefault();
            insertAtCaret(box.current, document.createElement("br"));
            changed();
          }}
        />
      </div>
    </div>
  );
}

/**
 * One signature: the toolbar over a Plate editor loaded from `initial` (at mount, its
 * images as data: URLs), reporting each change as HTML, "" once it holds no text and no image. A
 * signature with layout (hasLayout) gets LayoutEditor instead. pickImage is SignaturePage.PickImage.
 * The editor scrolls inside the page; the HTML mode is native's (mode.onHTML).
 */
export function SignatureEditor(props: {
  initial: string;
  onChange(html: string): void;
  pickImage(): Promise<string>;
  t: T;
  mode?: Mode;
}) {
  const { initial, onChange, pickImage, t, mode = { onHTML() {} } } = props;
  if (hasLayout(initial))
    return <LayoutEditor t={t} initial={initial} mode={mode} onChange={onChange} />;
  return (
    <RichEditor initial={initial} onChange={onChange} pickImage={pickImage} t={t} mode={mode} />
  );
}

function RichEditor(props: {
  initial: string;
  onChange(html: string): void;
  pickImage(): Promise<string>;
  t: T;
  mode: Mode;
}) {
  const { initial, onChange, pickImage, t, mode } = props;
  const editor = usePlateEditor({ plugins, value: (e) => fromHTML(e, initial, true) });
  return (
    <Plate
      editor={editor}
      onValueChange={({ value }) =>
        onChange(toText(value).trim() || images(value) ? toHTML(value) : "")
      }
    >
      <div className="flex h-screen flex-col">
        <Toolbar t={t} mode={mode} pickImage={pickImage} />
        {/* The wrapper scrolls, not the editable: WebKit stops repainting a scrolling contenteditable. */}
        <div className="min-h-0 flex-1 overflow-auto">
          <PlateContent
            aria-label={t("settings.account.signature.label")}
            className="min-h-full px-3 py-2.5 leading-[1.6] outline-none [&_a]:text-primary [&_a]:underline"
          />
        </div>
      </div>
    </Plate>
  );
}

/**
 * The signature page (role "signature", embedded in Settings › Signatures): the signature native
 * shows, through signatureSaver. Listeners first, then State: a show event newer than its reply
 * wins.
 */
export function SignatureApp() {
  const [state, setState] = useState<SignatureState>();
  const [theme, setTheme] = useState<PageTheme>();
  const [doc, setDoc] = useState<Doc>();
  const [refused, setRefused] = useState<Refusal>();
  const saver = useMemo(() => signatureSaver(api, setDoc, setRefused), []);

  useEffect(() => {
    const offs = [
      events.signatureShow.on(saver.show),
      events.signatureFlush.on(saver.flush),
      events.theme.on((th) => setTheme(th as PageTheme)),
    ];
    const g = saver.gen();
    api.state().then((s) => {
      setState(s);
      setTheme((th) => th ?? (s.theme as PageTheme));
      if (s.signature && saver.gen() === g) saver.show(s.signature);
    });
    return () => {
      for (const off of offs) off();
    };
  }, [saver]);
  useEffect(() => (theme ? applyTheme(theme) : undefined), [theme]);

  const t = useT(state?.locale);
  if (!state || !doc) return <main />;
  if ("error" in doc)
    return (
      <main className="p-3 text-sm text-muted-foreground">{t("error.unknown", doc.error)}</main>
    );
  return (
    <main>
      <SignatureEditor
        key={doc.id}
        initial={doc.html}
        t={t}
        pickImage={() => api.pickImage()}
        onChange={(html) => saver.edit(doc.id, html)}
        mode={{
          alert: refused && REFUSED[refused],
          // The edit saved first: native reads the code from Go.
          onHTML: () => saver.settle().then(() => api.editHTML().catch(() => {})),
        }}
      />
    </main>
  );
}

const REFUSED: Record<Refusal, string> = {
  count: "settings.account.signature.too_many",
  image: "settings.account.signature.image_invalid",
};
