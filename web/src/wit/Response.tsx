import { cn } from "cn";
import { type KeyboardEvent, type ReactNode, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import {
  type Components,
  defaultRehypePlugins,
  defaultRemarkPlugins,
  Streamdown,
  type StreamdownProps,
} from "streamdown";
import "streamdown/styles.css";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import type { T } from "@/shared/i18n";

type Node = { type: string; value?: string; url?: string; children?: Node[] };
/** Raw HTML in the Markdown shows as its text: no element is ever made of it. */
const htmlAsText = () => (tree: Node) => {
  const walk = (n: Node) => {
    if (n.type === "html") n.type = "text";
    n.children?.forEach(walk);
  };
  walk(tree);
};
const CITE = "#cite-";
const CARD = "#card-";
/** A paragraph of "[[n]]" alone becomes a link to CARD+n: a mail card, before citeLinks sees it. */
const cardLinks = () => (tree: Node) => {
  for (const p of tree.children ?? []) {
    const only = p.type === "paragraph" && p.children?.length === 1 ? p.children[0] : undefined;
    const n =
      only?.type === "text" ? /^\[\[(\d+)\]\]$/.exec(only.value?.trim() ?? "")?.[1] : undefined;
    if (p.children && n)
      p.children = [
        { type: "link", url: CARD + n, children: [{ type: "text", value: `[[${n}]]` }] },
      ];
  }
};
/** Each "[n]" in the text (not in a link or code) becomes a link to CITE+n: a source marker. */
const citeLinks = () => (tree: Node) => {
  const walk = (n: Node) => {
    if (n.type === "link" || n.type === "linkReference" || !n.children) return;
    n.children = n.children.flatMap((c): Node[] => {
      if (c.type !== "text") {
        walk(c);
        return [c];
      }
      return (c.value ?? "")
        .split(/(\[\d+\])/)
        .filter(Boolean)
        .map((v) =>
          /^\[\d+\]$/.test(v)
            ? { type: "link", url: CITE + v.slice(1, -1), children: [{ type: "text", value: v }] }
            : { type: "text", value: v },
        );
    });
  };
  walk(tree);
};

// Model output is untrusted. Set explicitly, never Streamdown's defaults (rehype-raw and a harden
// that allows every protocol): raw HTML as text (and skipHtml, no rehype-raw), sanitized, no
// images, and links only for http, https and mailto, each through the confirmation below.
const remarkPlugins = [...Object.values(defaultRemarkPlugins), htmlAsText, cardLinks];
// An answer that cites by tag: a plain "[n]" in it is text, not a citation.
const legacyPlugins = [...remarkPlugins, citeLinks];

/** How an answer cites mail n (core citeTag): <mail id="n"/>, or <mail id="n" card/> for its card. */
const MAIL_TAG = /<mail\s+id="?(\d+)"?(\s+card)?\s*\/?>(?:\s*<\/mail>)?/g;

/**
 * text with its mail tags as the markers the plugins read: a card as a paragraph of [[n]], a
 * citation as a link to CITE+n. While it streams, a tag not yet whole shows nothing.
 * shortcut: a tag inside code is turned too; parse it in the tree if answers quote tags as code.
 */
function mailTags(text: string, streaming: boolean) {
  const whole = streaming ? text.replace(/<(?:m(?:a(?:i(?:l\b[^>]*)?)?)?)?$/, "") : text;
  return whole.replace(MAIL_TAG, (_, n: string, card?: string) =>
    card ? `\n\n[[${n}]]\n\n` : `[${n}](${CITE}${n})`,
  );
}

/** The text with its mail tags as "[n]": what copying an answer gives. */
export const plainCites = (text: string) => text.replace(MAIL_TAG, (_, n: string) => `[${n}]`);
type Pluggable = NonNullable<StreamdownProps["rehypePlugins"]>[number];
type Schema = { protocols?: Record<string, string[]>; tagNames?: string[] };
type Plugin = Extract<Pluggable, (...args: never[]) => unknown>;
const [sanitize, schema] = defaultRehypePlugins.sanitize as unknown as [Plugin, Schema];
// Two layers: the sanitizer keeps only http/https/mailto hrefs and no media element; the
// renderers below show no image and confirm every link.
const media = ["img", "picture", "source", "video", "audio", "track", "iframe", "embed", "object"];
const rehypePlugins: Pluggable[] = [
  [
    sanitize,
    {
      ...schema,
      protocols: { href: ["http", "https", "mailto"], cite: [], longDesc: [], src: [] },
      tagNames: schema.tagNames?.filter((n) => !media.includes(n)),
    },
  ],
];
const protocols = ["http:", "https:", "mailto:"];

/** href if it is an http, https or mailto URL, else undefined. */
export function safeLink(href: string | undefined): string | undefined {
  try {
    const u = new URL(href ?? "");
    return protocols.includes(u.protocol) ? u.href : undefined;
  } catch {
    return undefined;
  }
}

/** The address with its host in bold (all of a mailto's address). */
function Address({ url }: { url: string }) {
  const u = new URL(url);
  const host = u.host || url.slice(u.protocol.length);
  const i = url.indexOf(host);
  return (
    <span className="break-all">
      {url.slice(0, i)}
      <strong className="text-foreground">{host}</strong>
      {url.slice(i + host.length)}
    </span>
  );
}

/**
 * A source marker [n] (or what children show, such as a sender's chip): a popover with its card,
 * closed again by any action the card takes.
 */
export function Cite({
  n,
  card,
  children,
  className,
}: {
  n: number;
  card: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        // hovering shows the mail, as a source does in chat apps; a click or Enter still opens it
        openOnHover
        delay={150}
        aria-label={children ? undefined : `[${n}]`}
        render={
          <button
            type="button"
            className={
              className ??
              "mx-0.5 inline-flex h-5 min-w-5 cursor-pointer items-center justify-center rounded-sm bg-muted px-1.25 align-text-bottom text-[11px] text-muted-foreground tabular-nums leading-none hover:text-foreground data-popup-open:bg-border data-popup-open:text-foreground"
            }
          />
        }
      >
        {children ?? n}
      </PopoverTrigger>
      <PopoverContent
        side="bottom"
        align="start"
        className="w-80 gap-1.5 rounded-xl p-3 text-base"
        onClick={(e) => {
          if ((e.target as Element).closest("button, a")) setOpen(false);
        }}
      >
        {card}
      </PopoverContent>
    </Popover>
  );
}

/**
 * An answer: Markdown, hardened. A link opens only after its confirmation (link.open → onLink); a
 * mail tag <mail id="n"/> (in an answer from before tags, a marker [n]) opens a popover with what
 * cite(n) gives, if anything (else it is text); <mail id="n" card/> (before, a paragraph of [[n]]
 * alone) shows what card(n) gives in its place, if anything (else its text).
 */
export function Response({
  text,
  onLink,
  t,
  streaming = false,
  size = "panel",
  cite,
  card,
  confirm = true,
}: {
  text: string;
  onLink(url: string): void;
  /** false: a link goes straight to onLink, whose host confirms it (the Wit page: native's dialog). */
  confirm?: boolean;
  t: T;
  streaming?: boolean;
  /** "page": the Wit page's reading leading (1.75). */
  size?: "panel" | "page";
  cite?(n: number): ReactNode | undefined;
  card?(n: number): ReactNode | undefined;
}) {
  const [link, setLink] = useState<string>();
  const citeRef = useRef(cite);
  citeRef.current = cite;
  const cardRef = useRef(card);
  cardRef.current = card;
  const follow = useRef((_url: string) => {});
  follow.current = (url: string) => (confirm ? setLink(url) : onLink(url));
  const components = useMemo<Components>(
    () => ({
      img: () => null,
      // Headings a step above the text at most, as chat apps set them, not a document's sizes.
      h1: ({ children }: { children?: ReactNode }) => (
        <h1 className="pt-1.5 font-semibold text-lg">{children}</h1>
      ),
      h2: ({ children }: { children?: ReactNode }) => (
        <h2 className="pt-1.5 font-semibold text-lg">{children}</h2>
      ),
      h3: ({ children }: { children?: ReactNode }) => (
        <h3 className="pt-1 font-semibold">{children}</h3>
      ),
      h4: ({ children }: { children?: ReactNode }) => <h4 className="font-semibold">{children}</h4>,
      // One border: the table's, rounded, its header on the sidebar colour, rows ruled.
      table: ({ children }: { children?: ReactNode }) => (
        <div className="overflow-x-auto rounded-lg border-[0.5px]">
          <table className="w-full border-collapse">{children}</table>
        </div>
      ),
      th: ({ children }: { children?: ReactNode }) => (
        <th className="bg-sidebar px-3 py-2 text-left font-medium">{children}</th>
      ),
      td: ({ children }: { children?: ReactNode }) => <td className="px-3 py-2">{children}</td>,
      tr: ({ children }: { children?: ReactNode }) => (
        <tr className="border-muted not-first:border-t-[0.5px]">{children}</tr>
      ),
      // A card's paragraph is the card itself (a block, so not inside a <p>).
      p: ({ node, children }: { node?: unknown; children?: ReactNode }) => {
        const kids = (node as { children?: { properties?: { href?: unknown } }[] } | undefined)
          ?.children;
        const href = kids?.length === 1 ? kids[0].properties?.href : undefined;
        const shown =
          typeof href === "string" && href.startsWith(CARD)
            ? cardRef.current?.(Number(href.slice(CARD.length)))
            : undefined;
        return shown ? <div className="my-2">{shown}</div> : <p>{children}</p>;
      },
      a: ({ href, children }: { href?: string; children?: ReactNode }) => {
        if (href?.startsWith(CARD)) return <span>{children}</span>;
        if (href?.startsWith(CITE)) {
          const n = Number(href.slice(CITE.length));
          const card = citeRef.current?.(n);
          if (!card) return <span>{children}</span>;
          // Always n: a model-written [text](#cite-n) cannot dress the cited mail up as anything else.
          return <Cite n={n} card={card} />;
        }
        const url = safeLink(href);
        if (!url) return <span>{children}</span>;
        // No href: nothing opens the address but the confirmation, here or the host's (no
        // middle-click, no "open link").
        return (
          // biome-ignore lint/a11y/useSemanticElements: an <a> needs an href, which would open without the confirmation
          <span
            role="link"
            tabIndex={0}
            className="cursor-pointer text-primary underline underline-offset-3"
            onClick={() => follow.current(url)}
            onKeyDown={(e: KeyboardEvent) => {
              if (e.key !== "Enter" && e.key !== " ") return;
              e.preventDefault();
              follow.current(url);
            }}
          >
            {children}
          </span>
        );
      },
    }),
    [],
  );
  return (
    <>
      <Streamdown
        className={cn("space-y-2.5 text-md", size === "page" && "leading-[1.75]")}
        mode={streaming ? "streaming" : "static"}
        skipHtml
        remarkPlugins={/<mail\s+id=/.test(text) ? remarkPlugins : legacyPlugins}
        rehypePlugins={rehypePlugins}
        components={components}
        linkSafety={{ enabled: false }}
        controls={false}
      >
        {mailTags(text, streaming)}
      </Streamdown>
      <AlertDialog open={!!link} onOpenChange={(o) => !o && setLink(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("link.title")}</AlertDialogTitle>
            <AlertDialogDescription>{link && <Address url={link} />}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter className="border-t-[0.5px]">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <Button
              variant="outline"
              onClick={() => {
                if (link) void navigator.clipboard?.writeText(link);
                toast(t("link.copied"));
                setLink(undefined);
              }}
            >
              {t("link.copy")}
            </Button>
            <AlertDialogAction
              autoFocus
              onClick={() => {
                if (link) onLink(link);
                setLink(undefined);
              }}
            >
              {t("link.open")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
