import { RiInformationLine } from "@remixicon/react";
import { type ComponentProps, useEffect, useRef, useState } from "react";
import type { Locale } from "react-day-picker";
import {
  de,
  enUS,
  es,
  fr,
  it,
  ja,
  ko,
  nl,
  pl,
  pt,
  ru,
  tr,
  vi,
  zhCN,
  zhTW,
} from "react-day-picker/locale";
import {
  Dialog,
  DialogCancel,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/dialog";
import { Button } from "@/components/ui/button";
import { Calendar, CalendarDayButton } from "@/components/ui/calendar";
import { Input } from "@/components/ui/input";
import { currentLocale, type T } from "@/shared/i18n";

const day0 = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate());
const p2 = (n: number) => String(n).padStart(2, "0");

/** The calendar's locale for a language tag: the catalogs' languages, English for the rest. */
export function dayLocale(tag: string): Locale {
  const l = new Intl.Locale(tag).maximize();
  if (l.language === "zh") return l.script === "Hant" ? zhTW : zhCN;
  const all: Record<string, Locale> = { de, es, fr, it, ja, ko, nl, pl, pt, ru, tr, vi };
  return all[l.language] ?? enUS;
}

/**
 * shadcn's day button, focused as the arrow keys move: its own focus effect holds a ref the
 * generated code never attaches, so focus would stay on the first day and Enter pick that one.
 */
function DayButton(props: ComponentProps<typeof CalendarDayButton>) {
  const ref = useRef<HTMLButtonElement>(null);
  const focused = props.modifiers.focused;
  useEffect(() => {
    if (focused) ref.current?.focus();
  }, [focused]);
  // React 19 passes ref as a prop: CalendarDayButton spreads it onto its Button.
  return <CalendarDayButton {...props} {...({ ref } as object)} />;
}

/** at as the dialog's header shows it: "今天 · 11月15日周日 16:00", the relative day only for today and tomorrow. */
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

/**
 * Send later…: a calendar and a clock whose hands both drag, the time chosen read out on top,
 * and the note that only a running Godwit sends. Schedules at, unix ms, in the future.
 */
export function SendLaterDialog({
  open,
  onOpenChange,
  initial,
  off,
  onSchedule,
  t,
}: {
  open: boolean;
  onOpenChange(open: boolean): void;
  /** The time the dialog opens at. */
  initial: Date;
  off: boolean;
  onSchedule(at: number): void;
  t: T;
}) {
  const loc = currentLocale();
  const [day, setDay] = useState(() => day0(initial));
  const [mins, setMins] = useState(() => initial.getHours() * 60 + initial.getMinutes());
  const [past, setPast] = useState(false);
  const at = new Date(day.getFullYear(), day.getMonth(), day.getDate(), 0, mins);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="md" close={t("common.close")}>
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
            if (!(at.getTime() > Date.now())) {
              setPast(true);
              return;
            }
            onOpenChange(false);
            onSchedule(at.getTime());
          }}
        >
          <DialogHeader>
            <DialogTitle>{t("compose.send_later.title")}</DialogTitle>
            <p
              aria-live="polite"
              className={`text-sm tabular-nums ${past ? "text-destructive" : "text-muted-foreground"}`}
            >
              {past ? t("compose.send_later.past") : readout(loc, at)}
            </p>
          </DialogHeader>
          {/* A day is a large button (kit.SizeLg, Space(9)); the calendar is seven of them wide, the
              clock's face six tall, as the weeks are. Too narrow for both, the face goes under. */}
          <div className="grid grid-cols-[repeat(auto-fit,minmax(calc(var(--cell-size)*7),1fr))] items-start gap-4 [--cell-size:--spacing(9)]">
            <Calendar
              mode="single"
              required
              selected={day}
              onSelect={(d) => {
                setDay(d);
                setPast(false);
              }}
              defaultMonth={day}
              disabled={{ before: day0(new Date()) }}
              locale={dayLocale(loc)}
              components={{ DayButton }}
              aria-label={t("compose.send_later.date")}
              className="bg-transparent p-0 [--cell-size:inherit]"
            />
            <Clock
              mins={mins}
              label={t("compose.send_later.time")}
              onChange={(v) => {
                setMins(v);
                setPast(false);
              }}
            />
          </div>
          <DialogDescription className="flex items-center gap-1.5 text-xs">
            <RiInformationLine className="size-3.5 shrink-0" />
            {t("compose.send_later.note")}
          </DialogDescription>
          <DialogFooter>
            <DialogCancel>{t("common.cancel")}</DialogCancel>
            <Button type="submit" disabled={off}>
              {t("compose.send_later.schedule")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/**
 * The face's drawing in its viewBox, 100 across with the centre at 50: where the ticks run, the
 * numerals' circle, the hands' lengths and widths (thicker while dragged) and the hub. These are
 * the proportions of a clock, not of the layout; the face's size comes from the dialog.
 */
export const FACE = {
  c: 50,
  tick: { out: 47, major: 41, minor: 44 },
  numerals: 34,
  minute: { length: 40, width: 1.6, dragged: 2.2 },
  hour: { length: 25, width: 2.6, dragged: 3.2 },
  hub: 2.4,
};

/** The point of the face at turn (0 at 12, clockwise) and r from the centre, in viewBox units. */
export const facePoint = (turn: number, r: number) => [
  FACE.c + r * Math.sin(turn * 2 * Math.PI),
  FACE.c - r * Math.cos(turn * 2 * Math.PI),
];

/**
 * The time of day: a field to type it in, over a clock face whose hands both drag. Time runs on
 * through 12: the minute hand past 12 carries the hour, the hour hand past 12 turns morning to
 * afternoon. The minute hand snaps to 5 minutes; on the face ←→ step 5 minutes, ↑↓ an hour.
 */
export function Clock({
  mins,
  label,
  onChange,
}: {
  /** Minutes since midnight. */
  mins: number;
  label: string;
  onChange(mins: number): void;
}) {
  const h = Math.floor(mins / 60);
  const m = mins % 60;
  const pm = h >= 12;
  const put = (v: number) => onChange(((v % 1440) + 1440) % 1440);
  const [mx, my] = facePoint(m / 60, FACE.minute.length);
  const [hx, hy] = facePoint(((h % 12) + m / 60) / 12, FACE.hour.length);
  const [grab, setGrab] = useState<"h" | "m" | null>(null);
  // The field's text: the clock's time, unless a time is half typed in it.
  const hm = `${p2(h)}:${p2(m)}`;
  const [typed, setTyped] = useState(hm);
  const [shown, setShown] = useState(hm);
  if (shown !== hm) {
    // The time changed (the face, the keys, a typed time): the field shows it.
    setShown(hm);
    setTyped(hm);
  }
  // The pointer in viewBox units, and its turn round the centre.
  const at = (e: React.PointerEvent<HTMLDivElement>) => {
    const b = e.currentTarget.getBoundingClientRect();
    const x = ((e.clientX - b.left) / b.width) * 100;
    const y = ((e.clientY - b.top) / b.height) * 100;
    const a = Math.atan2(x - FACE.c, FACE.c - y);
    return { x, y, turn: (a < 0 ? a + 2 * Math.PI : a) / (2 * Math.PI) };
  };
  const move = (hand: "h" | "m", turn: number) => {
    // The nearest way round from the hand's place to the pointer, so the time runs on through 12.
    const wrap = (d: number, n: number) => ((((d + n / 2) % n) + n) % n) - n / 2;
    if (hand === "m") put(mins + wrap((Math.round(turn * 12) % 12) * 5 - m, 60));
    else put(mins + wrap(Math.round(turn * 12 - m / 60) - (h % 12), 12) * 60);
  };
  const hand = (x: number, y: number, w: number) => (
    <line
      x1={FACE.c}
      y1={FACE.c}
      x2={x}
      y2={y}
      className="stroke-foreground"
      strokeWidth={w}
      strokeLinecap="round"
    />
  );
  return (
    // The field level with the calendar's month, the face centred beside the days.
    <div className="flex flex-col items-center gap-4 self-stretch">
      {/* The time to type: the system's time field (segments, digits, ↑↓, the day period as the
          system words it), in step with the face. */}
      <div className="flex h-(--cell-size) items-center">
        <Input
          type="time"
          // A cleared or half-typed field schedules nothing, not the time before.
          required
          aria-label={label}
          value={typed}
          onChange={(e) => {
            // Mid-typing the field is "" (a segment half done): keep what is typed, so React does
            // not put the old time back under the cursor; a whole time sets the clock.
            setTyped(e.target.value);
            const [hh, mm] = e.target.value.split(":").map(Number);
            if (Number.isInteger(hh) && Number.isInteger(mm)) put(hh * 60 + mm);
          }}
          className="w-auto tabular-nums"
        />
      </div>
      <div
        role="slider"
        tabIndex={0}
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={1439}
        aria-valuenow={mins}
        aria-valuetext={`${h}:${p2(m)}`}
        className={`relative my-auto aspect-square w-[calc(var(--cell-size)*6)] touch-none select-none rounded-full outline-none focus-visible:ring-3 focus-visible:ring-ring/50 ${grab ? "cursor-grabbing" : "cursor-grab"}`}
        onKeyDown={(e) => {
          const d = { ArrowRight: 5, ArrowLeft: -5, ArrowUp: 60, ArrowDown: -60 }[e.key];
          if (d) {
            e.preventDefault();
            put(mins + d);
          }
        }}
        onPointerDown={(e) => {
          const { x, y, turn } = at(e);
          // The hand whose tip is nearer; a press elsewhere on the face moves the minutes.
          const near = Math.hypot(x - hx, y - hy) < Math.hypot(x - mx, y - my) ? "h" : "m";
          setGrab(near);
          e.currentTarget.setPointerCapture?.(e.pointerId);
          move(near, turn);
        }}
        onPointerMove={(e) => grab && move(grab, at(e).turn)}
        onPointerUp={() => setGrab(null)}
        onPointerCancel={() => setGrab(null)}
      >
        <svg viewBox="0 0 100 100" aria-hidden="true" className="block size-full">
          {/* The rim and the ticks in CSS pixels whatever the face's size: a hairline (kit's
              Hairline) and, every five minutes, a control's border (theme.Stroke). */}
          <circle
            cx={FACE.c}
            cy={FACE.c}
            r={FACE.c}
            className={pm ? "fill-muted" : "fill-background stroke-border"}
            strokeWidth={0.5}
            vectorEffect="non-scaling-stroke"
          />
          {[...Array(60).keys()].map((i) => {
            const major = i % 5 === 0;
            const [x1, y1] = facePoint(i / 60, FACE.tick.out);
            const [x2, y2] = facePoint(i / 60, major ? FACE.tick.major : FACE.tick.minor);
            return (
              <line
                key={i}
                x1={x1}
                y1={y1}
                x2={x2}
                y2={y2}
                className={major ? "stroke-foreground" : "stroke-muted-foreground"}
                strokeWidth={major ? 1 : 0.5}
                vectorEffect="non-scaling-stroke"
              />
            );
          })}
          {hand(hx, hy, grab === "h" ? FACE.hour.dragged : FACE.hour.width)}
          {hand(mx, my, grab === "m" ? FACE.minute.dragged : FACE.minute.width)}
          <circle cx={FACE.c} cy={FACE.c} r={FACE.hub} className="fill-foreground" />
        </svg>
        {/* The numerals in the interface's text, placed in percent of the face (its viewBox). */}
        {[12, 3, 6, 9].map((n) => {
          const [x, y] = facePoint(n / 12, FACE.numerals);
          return (
            <span
              key={n}
              aria-hidden="true"
              style={{ left: `${x}%`, top: `${y}%` }}
              className="pointer-events-none absolute -translate-x-1/2 -translate-y-1/2 text-muted-foreground text-xs tabular-nums"
            >
              {n}
            </span>
          );
        })}
      </div>
    </div>
  );
}
