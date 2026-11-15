import { RiArrowDownSLine } from "@remixicon/react";
import { useState } from "react";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { ButtonGroup } from "@/components/ui/button-group";
import { Input } from "@/components/ui/input";
import { currentLocale, type T } from "@/shared/i18n";

/** A datetime-local value (local time, to the minute) for d: date, "T", time. */
export function localInput(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** The presets Send later offers at now: tonight at 21:00 while it is before then, tomorrow at 8:00. */
export function presets(now: Date): { key: string; at: Date }[] {
  const [y, m, d] = [now.getFullYear(), now.getMonth(), now.getDate()];
  const tonight = new Date(y, m, d, 21, 0);
  return [
    ...(now < tonight ? [{ key: "compose.send_tonight", at: tonight }] : []),
    { key: "compose.send_tomorrow", at: new Date(y, m, d + 1, 8, 0) },
  ];
}

/**
 * The send button, at the bottom bar's end (ml-auto keeps it right when the bar wraps), split
 * with a native menu: send now, the presets, and Send later…, a dialog for any date and time
 * that says only a running Godwit sends.
 */
export function SendBar({
  sending,
  frozen,
  onSend,
  onSendLater,
  menu,
  t,
}: {
  sending: boolean;
  /** True while any operation runs: the buttons are inert. */
  frozen: boolean;
  onSend(): void;
  /** Sends at at, unix milliseconds. */
  onSendLater(at: number): void;
  /** Shows items ("" a separator) as a native menu at x, y of the page: the index chosen, -1 none. */
  menu(items: string[], x: number, y: number): Promise<number>;
  t: T;
}) {
  const [dialog, setDialog] = useState(false);
  const [date, setDate] = useState("");
  const [time, setTime] = useState("");
  const [past, setPast] = useState(false);
  const openDialog = () => {
    const next = new Date();
    next.setHours(next.getHours() + 1, 0, 0, 0); // the next whole hour
    const [d, tm] = localInput(next).split("T");
    setDate(d);
    setTime(tm);
    setPast(false);
    setDialog(true);
  };
  const options = async (e: React.MouseEvent<HTMLElement>) => {
    const hm = new Intl.DateTimeFormat(currentLocale(), { hour: "numeric", minute: "2-digit" });
    const ps = presets(new Date());
    const items = [t("compose.send_now"), "", ...ps.map((p) => t(p.key, hm.format(p.at))), ""];
    const r = e.currentTarget.getBoundingClientRect();
    const i = await menu(
      [...items, t("compose.send_later")],
      Math.round(r.left),
      Math.round(r.bottom),
    );
    if (i === 0) onSend();
    else if (i === items.length) openDialog();
    else if (i >= 2 && i < 2 + ps.length) onSendLater(ps[i - 2].at.getTime());
  };
  const off = sending || frozen;
  return (
    <ButtonGroup className="ml-auto">
      <Button disabled={off} onClick={onSend}>
        {t("compose.send")}
      </Button>
      <Button
        className="w-6"
        size="icon"
        disabled={off}
        aria-label={t("compose.send_options")}
        title={t("compose.send_options")}
        onClick={options}
      >
        <RiArrowDownSLine />
      </Button>
      <AlertDialog open={dialog} onOpenChange={setDialog}>
        <AlertDialogContent>
          <form
            className="contents"
            onKeyDown={(e) => {
              // Keys stay in the dialog (it is portaled, React bubbles them to the compose page):
              // ⌘↩ schedules here instead of the page's send now.
              e.stopPropagation();
              if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
                e.preventDefault();
                e.currentTarget.requestSubmit();
              }
            }}
            onSubmit={(e) => {
              e.preventDefault();
              const at = new Date(`${date}T${time}`).getTime(); // local time
              if (!(at > Date.now())) {
                setPast(true);
                return;
              }
              setDialog(false);
              onSendLater(at);
            }}
          >
            <AlertDialogHeader>
              <AlertDialogTitle>{t("compose.send_later.title")}</AlertDialogTitle>
              <AlertDialogDescription>{t("scheduled.note")}</AlertDialogDescription>
            </AlertDialogHeader>
            <div className="grid grid-cols-2 gap-2">
              <Input
                type="date"
                required
                value={date}
                aria-label={t("compose.send_later.date")}
                aria-invalid={past}
                onChange={(e) => {
                  setDate(e.target.value);
                  setPast(false);
                }}
              />
              <Input
                type="time"
                required
                value={time}
                aria-label={t("compose.send_later.time")}
                aria-invalid={past}
                onChange={(e) => {
                  setTime(e.target.value);
                  setPast(false);
                }}
              />
            </div>
            {past && <p className="text-destructive text-xs">{t("compose.send_later.past")}</p>}
            <AlertDialogFooter>
              <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
              <Button type="submit" disabled={off}>
                {t("compose.send_later.schedule")}
              </Button>
            </AlertDialogFooter>
          </form>
        </AlertDialogContent>
      </AlertDialog>
    </ButtonGroup>
  );
}
