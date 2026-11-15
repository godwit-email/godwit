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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
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
 * with a menu: send now, the presets, and Send later…, a dialog for any date and time that says
 * only a running Godwit sends.
 */
export function SendBar({
  sending,
  frozen,
  onSend,
  onSendLater,
  t,
}: {
  sending: boolean;
  /** True while any operation runs: the buttons are inert. */
  frozen: boolean;
  onSend(): void;
  /** Sends at at, unix milliseconds. */
  onSendLater(at: number): void;
  t: T;
}) {
  const [dialog, setDialog] = useState(false);
  const [date, setDate] = useState("");
  const [time, setTime] = useState("");
  const [past, setPast] = useState(false);
  const [now, setNow] = useState(() => new Date());
  const hm = new Intl.DateTimeFormat(currentLocale(), { hour: "numeric", minute: "2-digit" });
  const openDialog = () => {
    const next = new Date();
    next.setHours(next.getHours() + 1, 0, 0, 0); // the next whole hour
    const [d, tm] = localInput(next).split("T");
    setDate(d);
    setTime(tm);
    setPast(false);
    setDialog(true);
  };
  const off = sending || frozen;
  return (
    <div className="ml-auto flex items-center">
      <Button className="rounded-r-none" disabled={off} onClick={onSend}>
        {t("compose.send")}
      </Button>
      <DropdownMenu onOpenChange={(o) => o && setNow(new Date())}>
        <DropdownMenuTrigger
          render={
            <Button
              className="w-6 rounded-l-none border-l-primary-foreground/20"
              size="icon"
              disabled={off}
              aria-label={t("compose.send_options")}
              title={t("compose.send_options")}
            />
          }
        >
          <RiArrowDownSLine />
        </DropdownMenuTrigger>
        <DropdownMenuContent side="top" align="end" className="w-auto">
          <DropdownMenuItem onClick={onSend}>{t("compose.send_now")}</DropdownMenuItem>
          <DropdownMenuSeparator />
          {presets(now).map((p) => (
            <DropdownMenuItem key={p.key} onClick={() => onSendLater(p.at.getTime())}>
              {t(p.key, hm.format(p.at))}
            </DropdownMenuItem>
          ))}
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={openDialog}>{t("compose.send_later")}</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <AlertDialog open={dialog} onOpenChange={setDialog}>
        <AlertDialogContent>
          <form
            className="contents"
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
    </div>
  );
}
