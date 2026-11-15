import { RiArrowDownSLine } from "@remixicon/react";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { ButtonGroup } from "@/components/ui/button-group";
import { currentLocale, type T } from "@/shared/i18n";
import { SendLaterDialog } from "./SendLaterDialog";

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
 * with a native menu: send now, the presets, and Send later…, a dialog for any date and time.
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
  const [initial, setInitial] = useState(() => new Date());
  const openDialog = () => {
    const next = new Date();
    next.setHours(next.getHours() + 1, 0, 0, 0); // the next whole hour
    setInitial(next);
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
      <SendLaterDialog
        key={initial.getTime()}
        open={dialog}
        onOpenChange={setDialog}
        initial={initial}
        off={off}
        onSchedule={onSendLater}
        t={t}
      />
    </ButtonGroup>
  );
}
