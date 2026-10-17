import { BoldPlugin, ItalicPlugin, UnderlinePlugin } from "@platejs/basic-nodes/react";
import { upsertLink } from "@platejs/link";
import {
  type RemixiconComponentType,
  RiBold,
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
import { useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { fromHTML, IMAGE_SRC, safeHref, toHTML, toText } from "@/compose/editor/html";
import { linkPlugin, SafePastePlugin } from "@/compose/editor/plugins";
import { SignaturePage as api, events, type SignatureState } from "@/mygo";
import { type T, useT } from "@/shared/i18n";
import { applyTheme, type Theme as PageTheme } from "@/shared/theme";
import { type Doc, signatureSaver } from "./saver";

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

/**
 * 加粗, 斜体, 下划线 | 链接, 插入图片; the link's address is asked inline (the page is too short for a
 * popover). An image Go refused is said beside the buttons.
 */
function Toolbar({ t, pickImage }: { t: T; pickImage(): Promise<string> }) {
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

  const item = (
    key: string,
    label: string,
    Icon: RemixiconComponentType,
    run: () => void,
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
  // The row spans the card; ToggleGroup itself is fit-to-content and rounded.
  return (
    <div
      data-slot="signature-toolbar"
      className="flex items-center border-b-[0.5px] bg-sidebar px-1.5 py-1"
    >
      <ToggleGroup multiple value={value} size="sm" spacing={0.5}>
        {marks.map((m) =>
          item(m.key, m.label, m.Icon, () => {
            editor.tf.toggleMark(m.key);
            editor.tf.focus();
          }),
        )}
        <span className="mx-1 h-4 border-l-[0.5px]" />
        {item("link", "compose.link", RiLink, () => {
          saved.current = editor.selection;
          setBad(false);
          setLink("");
        })}
        {item(
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
      {refused && (
        <span role="alert" className="ml-2 min-w-0 truncate text-xs text-destructive">
          {t("settings.account.signature.image_invalid")}
        </span>
      )}
    </div>
  );
}

/**
 * One account's signature: the toolbar over a Plate editor loaded from `initial` (at mount, its
 * images as data: URLs), reporting each change as HTML, "" once it holds no text and no image.
 * pickImage is SignaturePage.PickImage. The editor scrolls inside the page.
 */
export function SignatureEditor(props: {
  initial: string;
  onChange(html: string): void;
  pickImage(): Promise<string>;
  t: T;
}) {
  const { initial, onChange, pickImage, t } = props;
  const editor = usePlateEditor({ plugins, value: (e) => fromHTML(e, initial, true) });
  return (
    <Plate
      editor={editor}
      onValueChange={({ value }) =>
        onChange(toText(value).trim() || images(value) ? toHTML(value) : "")
      }
    >
      <div className="flex h-screen flex-col">
        <Toolbar t={t} pickImage={pickImage} />
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
 * The signature page (role "signature", embedded in the account settings): the account native
 * shows, through signatureSaver. Listeners first, then State: an account event newer than its
 * reply wins.
 */
export function SignatureApp() {
  const [state, setState] = useState<SignatureState>();
  const [theme, setTheme] = useState<PageTheme>();
  const [doc, setDoc] = useState<Doc>();
  const saver = useMemo(() => signatureSaver(api, setDoc), []);

  useEffect(() => {
    const offs = [
      events.signatureAccount.on(saver.show),
      events.signatureFlush.on(saver.flush),
      events.theme.on((th) => setTheme(th as PageTheme)),
    ];
    const g = saver.gen();
    api.state().then((s) => {
      setState(s);
      setTheme((th) => th ?? (s.theme as PageTheme));
      if (s.account && saver.gen() === g) saver.show(s.account);
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
        key={doc.account}
        initial={doc.html}
        t={t}
        pickImage={() => api.pickImage()}
        onChange={(html) => saver.edit(doc.account, html)}
      />
    </main>
  );
}
