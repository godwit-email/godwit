import { BoldPlugin, ItalicPlugin, UnderlinePlugin } from "@platejs/basic-nodes/react";
import { upsertLink } from "@platejs/link";
import {
  type RemixiconComponentType,
  RiBold,
  RiItalic,
  RiLink,
  RiUnderline,
} from "@remixicon/react";
import type { SlateEditor, TRange } from "platejs";
import {
  Plate,
  PlateContent,
  useEditorRef,
  useEditorSelector,
  usePlateEditor,
} from "platejs/react";
import { useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { fromHTML, safeHref, toHTML, toText } from "@/compose/editor/html";
import { linkPlugin, SafePastePlugin } from "@/compose/editor/plugins";
import { SignaturePage as api, events, type SignatureState } from "@/mygo";
import { makeT, type T } from "@/shared/i18n";
import { applyTheme, type Theme as PageTheme } from "@/shared/theme";
import { type Doc, signatureSaver } from "./saver";

// The signature's formats: bold, italic, underline, link. No lists, no images.
const plugins = [BoldPlugin, ItalicPlugin, UnderlinePlugin, linkPlugin, SafePastePlugin];

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

/** 加粗, 斜体, 下划线 | 链接; the link's address is asked inline (the page is too short for a popover). */
function Toolbar({ t }: { t: T }) {
  const editor = useEditorRef();
  const value = useEditorSelector(active, []).split(" ").filter(Boolean);
  const [link, setLink] = useState<string>();
  const [bad, setBad] = useState(false);
  const saved = useRef<TRange | null>(null);

  if (link !== undefined)
    return (
      <form
        className="flex items-center gap-1 border-b bg-sidebar px-1.5 py-1"
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

  const item = (key: string, label: string, Icon: RemixiconComponentType, run: () => void) => (
    <ToggleGroupItem
      key={key}
      value={key}
      aria-label={t(label)}
      title={t(label)}
      className={button}
      onMouseDown={(e) => e.preventDefault()} // keep the editor's selection
      onClick={run}
    >
      <Icon className="size-4" />
    </ToggleGroupItem>
  );
  return (
    <ToggleGroup
      multiple
      value={value}
      size="sm"
      spacing={0.5}
      className="flex items-center border-b bg-sidebar px-1.5 py-1"
    >
      {marks.map((m) =>
        item(m.key, m.label, m.Icon, () => {
          editor.tf.toggleMark(m.key);
          editor.tf.focus();
        }),
      )}
      <span className="mx-1 h-4 w-px bg-border" />
      {item("link", "compose.link", RiLink, () => {
        saved.current = editor.selection;
        setBad(false);
        setLink("");
      })}
    </ToggleGroup>
  );
}

/**
 * One account's signature: the toolbar over a Plate editor loaded from `initial` (at mount),
 * reporting each change as HTML, "" once it holds no text. The editor scrolls inside the page.
 */
export function SignatureEditor(props: { initial: string; onChange(html: string): void; t: T }) {
  const { initial, onChange, t } = props;
  const editor = usePlateEditor({ plugins, value: (e) => fromHTML(e, initial) });
  return (
    <Plate
      editor={editor}
      onValueChange={({ value }) => onChange(toText(value).trim() ? toHTML(value) : "")}
    >
      <div className="flex h-screen flex-col">
        <Toolbar t={t} />
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
      events.signatureText.on((text) => setState((s) => s && { ...s, text })),
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

  const t = useMemo(() => makeT(state?.text ?? {}), [state]);
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
        onChange={(html) => saver.edit(doc.account, html)}
      />
    </main>
  );
}
