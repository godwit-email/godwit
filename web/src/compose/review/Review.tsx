import { RiSparkling2Line } from "@remixicon/react";
import { type FocusEvent, type ReactNode, useState } from "react";
import { Button } from "@/components/ui/button";
import type { ReviewBlock, ReviewOp, Review as ReviewT, ReviewToken } from "@/mygo";
import type { T } from "@/shared/i18n";
import { sanitize } from "../editor/html";

/** key.one or key.other by n, as the native i18n.Plural. */
export const plural = (t: T, key: string, n: number) => t(`${key}.${n === 1 ? "one" : "other"}`, n);

/** The panel card's line for a review: how many changes, or how much was rewritten. */
export function reviewSummary(r: ReviewT, t: T): string {
  if (r.inline) return plural(t, "wit.compose.review.inline", r.changes);
  return r.exact
    ? t("wit.compose.review.rewrite", Math.round(r.ratio * 100))
    : t("wit.compose.review.rewrite_plain");
}

// Editable HTML shown read-only: sanitized (no loader, no attribute but href), links inert.
function inert(html: string): string {
  const body = sanitize(html);
  for (const a of body.querySelectorAll("a")) {
    const s = body.ownerDocument.createElement("span");
    s.className = "text-primary underline";
    s.append(...a.childNodes);
    a.replaceWith(s);
  }
  return body.innerHTML;
}

function Static({
  html,
  label,
  className = "text-lg",
}: {
  html: string;
  label?: string;
  className?: string;
}) {
  return (
    <section
      aria-label={label}
      className={`${className} leading-[1.75] [&>*+*]:mt-3 [&_blockquote]:border-l-2 [&_blockquote]:pl-3 [&_blockquote]:text-muted-foreground [&_ol]:list-decimal [&_ol]:pl-6 [&_ul]:list-disc [&_ul]:pl-6`}
      // biome-ignore lint/security/noDangerouslySetInnerHtml: inert() keeps only the format set's tags, no attribute, no link
      dangerouslySetInnerHTML={{ __html: inert(html) }}
    />
  );
}

function Tokens({ tokens }: { tokens: ReviewToken[] }) {
  return tokens.map((tk, i) => {
    let n: ReactNode = tk.text;
    if (tk.italic) n = <em>{n}</em>;
    if (tk.bold) n = <strong>{n}</strong>;
    if (tk.href) n = <span className="text-primary underline">{n}</span>;
    // biome-ignore lint/suspicious/noArrayIndexKey: tokens are fixed for a review
    return <span key={i}>{n}</span>;
  });
}

const INS = "rounded-sm bg-ins text-ins-foreground no-underline";
const DEL = "rounded-sm bg-del text-del-foreground line-through";

/** An op as decided (d: 0 open, 1 accepted, -1 rejected): accepted words read as text, rejected ones go. */
function Op({ op, d }: { op: ReviewOp; d: number }) {
  if (op.kind === "same") return <Tokens tokens={op.tokens} />;
  const keep = op.kind === "ins" ? d === 1 : d === -1;
  if (d !== 0) return keep ? <Tokens tokens={op.tokens} /> : null;
  const Tag = op.kind === "ins" ? "ins" : "del";
  return (
    <>
      <Tag className={op.kind === "ins" ? INS : DEL}>
        <Tokens tokens={op.tokens} />
      </Tag>
      {op.link && <ins className={INS}> → {op.link}</ins>}
    </>
  );
}

// A block's runs: the ops of one change together (with the blank words between them), the rest alone.
function segments(b: ReviewBlock): { k: number; ops: ReviewOp[] }[] {
  if (b.whole >= 0) return [{ k: b.whole, ops: b.ops }];
  const out: { k: number; ops: ReviewOp[] }[] = [];
  for (let i = 0; i < b.ops.length; ) {
    const k = b.ops[i].change;
    const last = k < 0 ? i : b.ops.findLastIndex((o) => o.change === k);
    out.push({ k, ops: b.ops.slice(i, last + 1) });
    i = last + 1;
  }
  return out;
}

function blockClass(b: ReviewBlock): string {
  switch (b.kind) {
    case "heading":
      return "font-semibold text-xl";
    case "quote":
      return "border-l-2 pl-3 text-muted-foreground";
    case "listItem":
      return "relative pl-6";
    case "pre":
      return "font-mono";
  }
  return "";
}

/**
 * A pending Wit edit in place of the body. Inline: the new text with deletions and insertions
 * marked; hovering a change or focusing it offers 接受 / 拒绝, and the last decision calls onFinish
 * with every change's (1 or -1). A rewrite: the new version read-only, or both side by side
 * (对比原文). 全部接受 / 全部拒绝 call onDecide. `busy` disables the decisions (one is in flight).
 */
export function Review({
  review: r,
  busy,
  onDecide,
  onFinish,
  t,
}: {
  review: ReviewT;
  busy: boolean;
  onDecide(accept: boolean): void;
  onFinish(decisions: number[]): void;
  t: T;
}) {
  const [decided, setDecided] = useState<number[]>(() => Array(r.changes).fill(0));
  const [active, setActive] = useState(-1);
  const [compare, setCompare] = useState(false);
  const open = decided.filter((d) => d === 0).length;

  const decide = (k: number, d: number) => {
    const next = decided.map((x, i) => (i === k ? d : x));
    setDecided(next);
    setActive(-1);
    if (next.every((x) => x !== 0)) onFinish(next);
  };
  const leave = (e: FocusEvent) => {
    if (!e.currentTarget.contains(e.relatedTarget)) setActive(-1);
  };

  let n = 0; // an ordered list item's number
  const blocks = r.blocks.map((b, bi) => {
    const dw = b.whole >= 0 ? decided[b.whole] : 0;
    if ((b.added && dw === -1) || (b.removed && dw === 1)) return null;
    n = b.kind === "listItem" && b.ordered ? n + 1 : 0;
    const marker = b.kind === "listItem" && (
      <span className="absolute left-2 text-muted-foreground">{b.ordered ? `${n}.` : "•"}</span>
    );
    return (
      // biome-ignore lint/suspicious/noArrayIndexKey: blocks are fixed for a review
      <div key={bi} className={`whitespace-pre-wrap ${blockClass(b)}`}>
        {marker}
        {segments(b).map((s, si) => {
          const ops = s.ops.map((op, oi) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: ops are fixed for a review
            <Op key={oi} op={op} d={s.k >= 0 ? decided[s.k] : 0} />
          ));
          if (s.k < 0 || decided[s.k] !== 0)
            // biome-ignore lint/suspicious/noArrayIndexKey: segments are fixed for a review
            return <span key={si}>{ops}</span>;
          const link = s.ops.find((o) => o.link)?.link;
          return (
            // A change: hover or focus (the caret's stand-in in a read-only view) offers its decision.
            // biome-ignore lint/a11y/noStaticElementInteractions: focusable; its buttons do the work
            <span
              // biome-ignore lint/suspicious/noArrayIndexKey: segments are fixed for a review
              key={si}
              data-change={s.k}
              // biome-ignore lint/a11y/noNoninteractiveTabindex: the caret's stand-in: a change can take focus
              tabIndex={0}
              className="relative outline-none"
              onMouseEnter={() => setActive(s.k)}
              onMouseLeave={() => setActive(-1)}
              onFocus={() => setActive(s.k)}
              onBlur={leave}
            >
              {ops.length > 0 ? ops : " "}
              {active === s.k && (
                <span className="absolute top-full left-0 z-10 mt-1 flex items-center gap-1 whitespace-nowrap rounded-lg border bg-popover p-1 text-base shadow-md">
                  {link && (
                    <span className="px-1 text-muted-foreground text-sm">
                      {t("wit.compose.link", link)}
                    </span>
                  )}
                  <Button size="xs" disabled={busy} onClick={() => decide(s.k, 1)}>
                    {t("wit.compose.accept")}
                  </Button>
                  <Button
                    size="xs"
                    variant="secondary"
                    disabled={busy}
                    onClick={() => decide(s.k, -1)}
                  >
                    {t("wit.compose.reject")}
                  </Button>
                </span>
              )}
            </span>
          );
        })}
      </div>
    );
  });

  const pct = Math.round(r.ratio * 100);
  return (
    <div className="flex flex-1 flex-col">
      <div className="flex items-center gap-2 bg-sidebar px-4 py-2 text-base">
        <RiSparkling2Line className="size-3.5 shrink-0" />
        <span className="flex-1">
          {r.inline
            ? plural(t, "wit.compose.pending", open)
            : r.exact
              ? t("wit.compose.rewrite", pct)
              : t("wit.compose.rewrite_plain")}
        </span>
        {!r.inline && (
          <Button
            size="sm"
            variant="outline"
            className="text-base"
            onClick={() => setCompare(!compare)}
          >
            {t(compare ? "wit.compose.back" : "wit.compose.compare")}
          </Button>
        )}
        <Button
          size="sm"
          variant="outline"
          className="text-base"
          disabled={busy}
          onClick={() => onDecide(false)}
        >
          {t("wit.compose.reject_all")}
        </Button>
        <Button size="sm" className="text-base" disabled={busy} onClick={() => onDecide(true)}>
          {t("wit.compose.accept_all")}
        </Button>
      </div>
      <section aria-label={t("wit.compose.review_body")} className="min-h-70 flex-1 p-4">
        {r.inline ? (
          <div className="text-lg leading-[1.75] [&>*+*]:mt-3">{blocks}</div>
        ) : compare ? (
          <div className="grid grid-cols-2 gap-4">
            <div className="flex flex-col gap-2 text-muted-foreground">
              <h2 className="font-semibold text-sm">{t("wit.compose.original")}</h2>
              <Static html={r.old} label={t("wit.compose.original")} className="text-md" />
            </div>
            <div className="flex flex-col gap-2 border-l pl-4">
              <h2 className="font-semibold text-sm">{t("wit.compose.new")}</h2>
              <Static html={r.new} label={t("wit.compose.new")} className="text-md" />
            </div>
          </div>
        ) : (
          <Static html={r.new} />
        )}
      </section>
    </div>
  );
}
