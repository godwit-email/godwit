import { type ReactNode, type Ref, useId, useImperativeHandle, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { Account } from "@/mygo";
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
 * The header fields: From, To; Cc, Bcc and Reply-To behind 抄送 · 密送 unless one has a value;
 * the invalid addresses; Subject.
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
  t: T;
}) {
  const id = useId();
  const [pending, setPending] = useState(none);
  const [ccOpen, setCcOpen] = useState(false);
  const lists: Record<AddressKey, string[]> = { to, cc, bcc, replyTo: replyTo ? [replyTo] : [] };

  const set = (k: AddressKey, l: string[]) =>
    onChange(k === "replyTo" ? { replyTo: l.at(-1) ?? "" } : { [k]: l });
  const commitOne = (k: AddressKey, text: string) => {
    setPending((p) => ({ ...p, [k]: "" }));
    const l = commit(text, lists[k], k === "replyTo");
    if (l !== lists[k]) set(k, l);
  };
  useImperativeHandle(ref, () => ({
    commitPending() {
      for (const k of Object.keys(none) as AddressKey[]) if (pending[k]) commitOne(k, pending[k]);
    },
  }));

  const row = (k: string, label: string, control: ReactNode) => (
    <div key={k} className="flex items-center gap-3 border-b px-5 py-2">
      <Label htmlFor={`${id}${k}`} className="w-16 shrink-0 font-normal text-muted-foreground">
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
          t={t}
        />
        {extra}
      </>,
    );
  const showCc = ccOpen || cc.length > 0 || bcc.length > 0 || replyTo !== "";
  const bad = invalid([...to, ...cc, ...bcc, ...lists.replyTo]);

  return (
    <div className="flex flex-col">
      {row(
        "from",
        t("compose.from"),
        <Select
          value={from}
          onValueChange={(v) => v !== null && onFrom(v)}
          items={accounts.map((a) => ({ value: a.id, label: a.email }))}
        >
          <SelectTrigger id={`${id}from`} aria-label={t("compose.from.select")} size="sm">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {accounts.map((a) => (
              <SelectItem key={a.id} value={a.id}>
                {a.email}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>,
      )}
      {address(
        "to",
        "compose.to",
        "compose.to.input",
        !showCc && (
          <Button variant="link" size="sm" onClick={() => setCcOpen(true)}>
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
        <p className="px-5 py-1.5 text-sm text-destructive">
          {t("compose.invalid", bad.join(", "))}
        </p>
      )}
      {row(
        "subject",
        t("compose.subject"),
        <Input
          id={`${id}subject`}
          className="h-7 flex-1 border-0 px-0 shadow-none focus-visible:ring-0"
          placeholder={t("compose.subject.input")}
          value={subject}
          onChange={(e) => onChange({ subject: e.target.value })}
        />,
      )}
    </div>
  );
}
