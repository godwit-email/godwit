import { type KeyboardEvent, type ReactNode, useMemo, useState } from "react";
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
import type { T } from "@/shared/i18n";

type Node = { type: string; value?: string; children?: Node[] };
/** Raw HTML in the Markdown shows as its text: no element is ever made of it. */
const htmlAsText = () => (tree: Node) => {
  const walk = (n: Node) => {
    if (n.type === "html") n.type = "text";
    n.children?.forEach(walk);
  };
  walk(tree);
};

// Model output is untrusted. Set explicitly, never Streamdown's defaults (rehype-raw and a harden
// that allows every protocol): raw HTML as text (and skipHtml, no rehype-raw), sanitized, no
// images, and links only for http, https and mailto, each through the confirmation below.
const remarkPlugins = [...Object.values(defaultRemarkPlugins), htmlAsText];
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

/** An answer: Markdown, hardened. A link opens only after its confirmation (link.open → onLink). */
export function Response({
  text,
  onLink,
  t,
  streaming = false,
}: {
  text: string;
  onLink(url: string): void;
  t: T;
  streaming?: boolean;
}) {
  const [link, setLink] = useState<string>();
  const components = useMemo<Components>(
    () => ({
      img: () => null,
      a: ({ href, children }: { href?: string; children?: ReactNode }) => {
        const url = safeLink(href);
        if (!url) return <span>{children}</span>;
        // No href: nothing opens the address but the confirmation (no middle-click, no "open link").
        return (
          // biome-ignore lint/a11y/useSemanticElements: an <a> needs an href, which would open without the confirmation
          <span
            role="link"
            tabIndex={0}
            className="cursor-pointer text-primary underline underline-offset-3"
            onClick={() => setLink(url)}
            onKeyDown={(e: KeyboardEvent) => {
              if (e.key !== "Enter" && e.key !== " ") return;
              e.preventDefault();
              setLink(url);
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
        className="text-base"
        mode={streaming ? "streaming" : "static"}
        skipHtml
        remarkPlugins={remarkPlugins}
        rehypePlugins={rehypePlugins}
        components={components}
        linkSafety={{ enabled: false }}
        controls={false}
      >
        {text}
      </Streamdown>
      <AlertDialog open={!!link} onOpenChange={(o) => !o && setLink(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("link.title")}</AlertDialogTitle>
            <AlertDialogDescription>{link && <Address url={link} />}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
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
