import { RiTimeLine } from "@remixicon/react";
import { useEffect, useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import type { T } from "@/shared/i18n";

/** A datetime-local value (local time, to the minute) for d. */
export function localInput(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** Tomorrow at 8:00, local time: the time Send later suggests. */
function tomorrowMorning(now: Date): Date {
  return new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, 8, 0);
}

/**
 * The send button, at the bottom bar's end (ml-auto keeps it right when the bar wraps),
 * after Send later: a date and time, and the note that only a running Godwit sends.
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
  const id = useId();
  const [open, setOpen] = useState(false);
  const [when, setWhen] = useState("");
  const [past, setPast] = useState(false);
  useEffect(() => {
    if (open) {
      setWhen(localInput(tomorrowMorning(new Date())));
      setPast(false);
    }
  }, [open]);
  return (
    <div className="ml-auto flex items-center gap-1">
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger
          render={
            <Button
              variant="ghost"
              size="icon"
              disabled={sending || frozen}
              aria-label={t("compose.send_later")}
              title={t("compose.send_later")}
            />
          }
        >
          <RiTimeLine />
        </PopoverTrigger>
        <PopoverContent side="top" align="end" className="w-80">
          <form
            className="flex flex-col gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              const at = new Date(when).getTime(); // datetime-local is local time
              if (!(at > Date.now())) {
                setPast(true);
                return;
              }
              setOpen(false);
              onSendLater(at);
            }}
          >
            <Label htmlFor={id} className="font-normal text-muted-foreground">
              {t("compose.send_later.when")}
            </Label>
            <Input
              id={id}
              type="datetime-local"
              value={when}
              aria-invalid={past}
              onChange={(e) => {
                setWhen(e.target.value);
                setPast(false);
              }}
            />
            {past && <p className="text-destructive text-xs">{t("compose.send_later.past")}</p>}
            <p className="text-muted-foreground text-xs">{t("scheduled.note")}</p>
            <Button type="submit" className="self-end" disabled={sending || frozen}>
              {t("compose.send_later.schedule")}
            </Button>
          </form>
        </PopoverContent>
      </Popover>
      <Button
        className="border-0 px-4 font-semibold text-md"
        disabled={sending || frozen}
        onClick={onSend}
      >
        {t("compose.send")}
      </Button>
    </div>
  );
}
