import { RiArrowDownSLine } from "@remixicon/react";
import { type ReactNode, type Ref, useId, useImperativeHandle, useRef, useState } from "react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { type PopupMenu, popupUnder } from "@/components/wit-menu";
import type { Account, ContactHit } from "@/mygo";
import type { T } from "@/shared/i18n";
import { AddressInput } from "./AddressInput";
import { commit, invalid } from "./address";

type AddressKey = "to" | "cc" | "bcc" | "replyTo";

/** A change to the header fields: only the fields that changed. */
export type FieldsEdit = Partial<{
  to: string[];
  cc: string[];
  bcc: string[];
  replyTo: string;
  subject: string;
}>;

/** What the page's settle hook needs: typed-but-uncommitted addresses become addresses. */
export interface FieldsHandle {
  commitPending(): void;
}

const none: Record<AddressKey, string> = { to: "", cc: "", bcc: "", replyTo: "" };

/**
 * The header fields: To; Cc, Bcc and Reply-To behind 抄送 · 密送 unless one has a value;
 * the invalid addresses; Subject; From. `frozen` (an operation running) makes them all read-only.
 */
export function Fields({
  ref,
  accounts,
  from,
  onFrom,
  menu,
  to,
  cc,
  bcc,
  replyTo,
  subject,
  onChange,
  onSearch,
  frozen = false,
  t,
}: {
  ref?: Ref<FieldsHandle>;
  accounts: Account[];
  from: number;
  onFrom(id: number): void;
  /** Shows the accounts as the system's menu (the pages' PopupMenu). */
  menu: PopupMenu;
  to: string[];
  cc: string[];
  bcc: string[];
  replyTo: string;
  subject: string;
  onChange(e: FieldsEdit): void;
  /** The contacts matching what is typed in an address field. */
  onSearch?(query: string): Promise<ContactHit[]>;
  frozen?: boolean;
  t: T;
}) {
  const id = useId();
  const frozenNow = useRef(frozen); // as the menu returns: the fields may have frozen while it was open
  frozenNow.current = frozen;
  const [pending, setPending] = useState(none);
  const [ccOpen, setCcOpen] = useState(false);
  const lists: Record<AddressKey, string[]> = { to, cc, bcc, replyTo: replyTo ? [replyTo] : [] };
  const fromEmail = accounts.find((a) => a.id === from)?.email;

  const set = (k: AddressKey, l: string[]) =>
    onChange(k === "replyTo" ? { replyTo: l.at(-1) ?? "" } : { [k]: l });
  const commitOne = (k: AddressKey, text: string) => {
    setPending((p) => ({ ...p, [k]: "" }));
    const l = commit(text, lists[k], k === "replyTo");
    if (l !== lists[k]) set(k, l);
  };
  useImperativeHandle(ref, () => ({
    commitPending() {
      if (frozen) return;
      for (const k of Object.keys(none) as AddressKey[]) if (pending[k]) commitOne(k, pending[k]);
    },
  }));

  const row = (k: string, label: string, control: ReactNode) => (
    <div
      key={k}
      className="col-span-2 grid grid-cols-subgrid items-center gap-x-3 border-b-[0.5px] py-2 text-sm"
    >
      <Label
        htmlFor={`${id}${k}`}
        className="whitespace-nowrap font-normal text-sm text-muted-foreground leading-5"
      >
        {label}
      </Label>
      <div className="flex min-w-0 items-center gap-3">{control}</div>
    </div>
  );
  const address = (k: AddressKey, label: string, placeholder: string, extra?: ReactNode) =>
    row(
      k,
      t(label),
      <>
        <AddressInput
          id={`${id}${k}`}
          list={lists[k]}
          pending={pending[k]}
          onPending={(s) => setPending((p) => ({ ...p, [k]: s }))}
          onCommit={(s) => commitOne(k, s)}
          onRemove={(a) =>
            set(
              k,
              lists[k].filter((x) => x !== a),
            )
          }
          placeholder={t(placeholder)}
          onSearch={onSearch}
          frozen={frozen}
          t={t}
        />
        {extra}
      </>,
    );
  const showCc = ccOpen || cc.length > 0 || bcc.length > 0 || replyTo !== "";
  const bad = invalid([...to, ...cc, ...bcc, ...lists.replyTo]);

  return (
    <div className="mx-4 grid grid-cols-[minmax(4em,auto)_1fr] text-sm">
      {address(
        "to",
        "compose.to",
        "compose.to.input",
        !showCc && (
          <button
            type="button"
            disabled={frozen}
            className="shrink-0 text-muted-foreground text-sm transition-colors hover:text-foreground disabled:pointer-events-none disabled:text-(--idle)"
            onClick={() => setCcOpen(true)}
          >
            {t("compose.cc_bcc")}
          </button>
        ),
      )}
      {showCc && (
        <>
          {address("cc", "compose.cc", "compose.cc.input")}
          {address("bcc", "compose.bcc", "compose.bcc.input")}
          {address("replyTo", "compose.reply_to", "compose.reply_to.input")}
        </>
      )}
      {bad.length > 0 && (
        <p className="col-span-2 border-b-[0.5px] py-2 text-xs text-destructive">
          {t("compose.invalid", bad.join(", "))}
        </p>
      )}
      {row(
        "subject",
        t("compose.subject"),
        <Input
          id={`${id}subject`}
          className="h-5.25 flex-1 rounded-none border-0 bg-transparent px-0.5 py-0 text-sm shadow-none focus-visible:ring-0 md:text-sm dark:bg-transparent"
          placeholder={t("compose.subject.input")}
          value={subject}
          readOnly={frozen}
          onChange={(e) => !frozen && onChange({ subject: e.target.value })}
        />,
      )}
      {row(
        "from",
        t("compose.from"),
        // The sending account: the pill is the value; the system's menu lists the accounts, the one
        // in use checked, as the design's menus are the system's.
        <button
          type="button"
          id={`${id}from`}
          aria-label={t("compose.from.select")}
          title={fromEmail}
          disabled={frozen}
          className="flex h-6 min-w-0 items-center gap-1 rounded-md bg-(--hover-tint) px-2 font-medium text-xs transition-colors hover:bg-(--tint-2) focus-visible:ring-3 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50"
          onClick={async (e) => {
            const chosen = await popupUnder(
              menu,
              e.currentTarget,
              accounts.map((a) => ({
                id: String(a.id),
                label: a.email,
                radio: true,
                checked: a.id === from,
              })),
            );
            if (chosen && !frozenNow.current && Number(chosen) !== from) onFrom(Number(chosen));
          }}
        >
          <span className="min-w-0 truncate">{fromEmail}</span>
          <RiArrowDownSLine className="size-3.5 shrink-0" />
        </button>,
      )}
    </div>
  );
}
