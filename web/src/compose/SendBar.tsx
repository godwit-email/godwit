import { RiArrowDownSLine } from "@remixicon/react";
import type { Ref } from "react";
import { Button } from "@/components/button";
import { ButtonGroup } from "@/components/ui/button-group";
import { type PopupMenu, popupUnder, separator } from "@/components/wit-menu";
import { currentLocale, type T } from "@/shared/i18n";

const day0 = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate());

/** at as send later reads it out: "今天 · 11月15日周日 16:00", the relative day only for today and tomorrow. */
export function readout(loc: string, at: Date, now = new Date()): string {
  const days = Math.round((day0(at).getTime() - day0(now).getTime()) / 864e5);
  const when = new Intl.DateTimeFormat(loc, {
    month: "long",
    day: "numeric",
    weekday: "short",
    hour: "numeric",
    minute: "2-digit",
  }).format(at);
  if (days !== 0 && days !== 1) return when;
  const rel = new Intl.RelativeTimeFormat(loc, { numeric: "auto" }).format(days, "day");
  return `${rel.charAt(0).toLocaleUpperCase(loc)}${rel.slice(1)} · ${when}`;
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
 * with a native menu: send now, the presets, and Send later…, native's dialog for any date and time.
 */
export function SendBar({
  anchor,
  sending,
  frozen,
  anyway = false,
  scheduledFor,
  onSend,
  onSendNow = onSend,
  onSendLater,
  menu,
  later,
  t,
}: {
  /** The buttons' box, which the toasts line their right edge up with. */
  anchor?: Ref<HTMLDivElement>;
  sending: boolean;
  /** True while any operation runs: the buttons are inert. */
  frozen: boolean;
  /** Plugins warned about this send: the button reads 仍然发送, and onSend goes ahead despite them. */
  anyway?: boolean;
  scheduledFor: number;
  onSend(): void;
  /** The menu's Send now: now, whatever onSend's warned send was for (onSend if absent). */
  onSendNow?(): void;
  /** Sends at at, unix milliseconds. */
  onSendLater(at: number): void;
  /** Shows entries as the system's menu: the id chosen, "" for none. */
  menu: PopupMenu;
  /** Shows native's send later dialog at at, unix ms: the time chosen, 0 for none. */
  later(at: number): Promise<number>;
  t: T;
}) {
  const openDialog = async () => {
    const next = new Date();
    if (scheduledFor > next.getTime()) next.setTime(scheduledFor);
    else next.setHours(next.getHours() + 1, 0, 0, 0);
    const at = await later(next.getTime());
    if (at > 0) onSendLater(at);
  };
  const options = async (e: React.MouseEvent<HTMLElement>) => {
    const loc = currentLocale();
    const hm = new Intl.DateTimeFormat(loc, { hour: "numeric", minute: "2-digit" });
    const now = new Date();
    const ps = presets(now);
    const choices = ps.map((p, i) => ({ id: `preset:${i}`, label: t(p.key, hm.format(p.at)) }));
    if (scheduledFor > now.getTime()) {
      choices.unshift({
        id: "again",
        label: t("compose.schedule_again", readout(loc, new Date(scheduledFor), now)),
      });
    }
    const id = await popupUnder(menu, e.currentTarget, [
      { id: "now", label: t("compose.send_now") },
      separator,
      ...choices,
      separator,
      { id: "later", label: t("compose.send_later") },
    ]);
    const preset = id.startsWith("preset:") ? ps[Number(id.slice("preset:".length))] : undefined;
    if (id === "now") onSendNow();
    else if (id === "again" && scheduledFor > Date.now()) onSendLater(scheduledFor);
    else if (id === "later" || id === "again") await openDialog();
    else if (preset) onSendLater(preset.at.getTime());
  };
  const off = sending || frozen;
  return (
    <ButtonGroup ref={anchor} className="ml-auto">
      <Button disabled={off} onClick={onSend}>
        {t(anyway ? "compose.send_anyway" : "compose.send")}
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
    </ButtonGroup>
  );
}
