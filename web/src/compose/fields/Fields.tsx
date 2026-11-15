import { RiArrowDownSLine } from "@remixicon/react";
import { type ReactNode, type Ref, useId, useImperativeHandle, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
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
    <div key={k} className="mx-4 flex items-center gap-3 border-b-[0.5px] py-2 text-sm">
      <Label
        htmlFor={`${id}${k}`}
        className="w-12 shrink-0 font-normal text-sm text-muted-foreground leading-5"
      >
        {label}
      </Label>
      {control}
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
    <div className="flex flex-col">
      {address(
        "to",
        "compose.to",
        "compose.to.input",
        !showCc && (
          <Button
            variant="link"
            size="sm"
            className="h-auto shrink-0 px-0 font-normal text-sm text-muted-foreground hover:text-foreground"
            disabled={frozen}
            onClick={() => setCcOpen(true)}
          >
            {t("compose.cc_bcc")}
          </Button>
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
        <p className="mx-4 border-b-[0.5px] py-2 text-xs text-destructive">
          {t("compose.invalid", bad.join(", "))}
        </p>
      )}
      {row(
        "subject",
        t("compose.subject"),
        <Input
          id={`${id}subject`}
          className="h-5.5 flex-1 rounded-none border-0 bg-transparent px-0.5 py-0 text-sm shadow-none focus-visible:ring-0 md:text-sm dark:bg-transparent"
          placeholder={t("compose.subject.input")}
          value={subject}
          readOnly={frozen}
          onChange={(e) => !frozen && onChange({ subject: e.target.value })}
        />,
      )}
      {row(
        "from",
        t("compose.from"),
        // The sending account: the pill is the value, and the app's own glass menu lists the accounts
        // (checked one at a time), so switching reads like the rest of the UI rather than an OS popup.
        <DropdownMenu modal={false}>
          <DropdownMenuTrigger
            id={`${id}from`}
            aria-label={t("compose.from.select")}
            title={fromEmail}
            disabled={frozen}
            className="flex h-5.5 min-w-0 items-center rounded-sm bg-muted px-2 text-sm leading-4 transition-colors hover:bg-accent focus-visible:ring-3 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 data-popup-open:bg-accent"
          >
            <span className="min-w-0 truncate">{fromEmail}</span>
            <RiArrowDownSLine className="ml-1 size-3.5 shrink-0 text-muted-foreground" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="max-h-80 w-auto min-w-52 max-w-80">
            <DropdownMenuRadioGroup
              value={from}
              onValueChange={(v) => !frozen && onFrom(Number(v))}
            >
              {accounts.map((a) => (
                <DropdownMenuRadioItem key={a.id} value={a.id} closeOnClick>
                  <span className="truncate">{a.email}</span>
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>,
      )}
    </div>
  );
}
