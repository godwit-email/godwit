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
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Calendar, CalendarDayButton } from "@/components/ui/calendar";
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

/** The locale's names for the morning and the afternoon (上午, 下午; AM, PM). */
function dayPeriods(loc: string): [string, string] {
  const f = new Intl.DateTimeFormat(loc, { hour: "numeric", hour12: true });
  const name = (h: number) =>
    f.formatToParts(new Date(2000, 0, 1, h)).find((p) => p.type === "dayPeriod")?.value;
  return [name(0) ?? "AM", name(12) ?? "PM"];
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
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      {/* data-size="md" (after the component's own) is styles.css's DialogMd width, for a form. */}
      <AlertDialogContent data-size="md">
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
          <AlertDialogHeader>
            <AlertDialogTitle>{t("compose.send_later.title")}</AlertDialogTitle>
            <p
              aria-live="polite"
              className={`text-sm tabular-nums ${past ? "text-destructive" : "text-muted-foreground"}`}
            >
              {past ? t("compose.send_later.past") : readout(loc, at)}
            </p>
          </AlertDialogHeader>
          <div className="flex flex-wrap items-center justify-evenly gap-8">
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
              className="bg-transparent p-0"
            />
            <Clock
              mins={mins}
              periods={dayPeriods(loc)}
              label={t("compose.send_later.time")}
              onChange={(v) => {
                setMins(v);
                setPast(false);
              }}
            />
          </div>
          <AlertDialogDescription className="flex items-center gap-1.5 text-xs">
            <RiInformationLine className="size-3.5 shrink-0" />
            {t("compose.send_later.note")}
          </AlertDialogDescription>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <Button type="submit" disabled={off}>
              {t("compose.send_later.schedule")}
            </Button>
          </AlertDialogFooter>
        </form>
      </AlertDialogContent>
    </AlertDialog>
  );
}

/**
 * A clock face whose hands both drag, over the minutes of a day. Time runs on through 12: the
 * minute hand past 12 carries the hour, the hour hand past 12 turns morning to afternoon. The
 * minute hand snaps to 5 minutes; ←→ step 5 minutes, ↑↓ an hour.
 */
export function Clock({
  mins,
  periods,
  label,
  onChange,
}: {
  /** Minutes since midnight. */
  mins: number;
  periods: [string, string];
  label: string;
  onChange(mins: number): void;
}) {
  const size = 184;
  const c = size / 2;
  const h = Math.floor(mins / 60);
  const m = mins % 60;
  const pm = h >= 12;
  const put = (v: number) => onChange(((v % 1440) + 1440) % 1440);
  const pos = (turn: number, r: number) => [
    c + r * Math.sin(turn * 2 * Math.PI),
    c - r * Math.cos(turn * 2 * Math.PI),
  ];
  const [mx, my] = pos(m / 60, 66);
  const [hx, hy] = pos(((h % 12) + m / 60) / 12, 42);
  const [grab, setGrab] = useState<"h" | "m" | null>(null);
  const turnAt = (e: React.PointerEvent<HTMLDivElement>) => {
    const b = e.currentTarget.getBoundingClientRect();
    const x = e.clientX - b.left;
    const y = e.clientY - b.top;
    const a = Math.atan2(x - c, c - y);
    return { x, y, turn: (a < 0 ? a + 2 * Math.PI : a) / (2 * Math.PI) };
  };
  const move = (hand: "h" | "m", turn: number) => {
    // The nearest way round from the hand's place to the pointer, so the time runs on through 12.
    const wrap = (d: number, n: number) => ((((d + n / 2) % n) + n) % n) - n / 2;
    if (hand === "m") put(mins + wrap((Math.round(turn * 12) % 12) * 5 - m, 60));
    else put(mins + wrap(Math.round(turn * 12 - m / 60) - (h % 12), 12) * 60);
  };
  return (
    <div className="flex flex-col items-center gap-3">
      <div className="flex items-center gap-2">
        <span className="font-medium text-3xl tabular-nums">
          {h % 12 === 0 ? 12 : h % 12}:{p2(m)}
        </span>
        <div className="flex flex-col rounded-md border p-0.5 text-xs">
          {periods.map((name, i) => (
            <button
              key={name}
              type="button"
              aria-pressed={pm === (i === 1)}
              onClick={() => put((h % 12) * 60 + i * 720 + m)}
              className={`rounded px-1.5 py-0.5 ${pm === (i === 1) ? "bg-muted font-medium" : "text-muted-foreground hover:text-foreground"}`}
            >
              {name}
            </button>
          ))}
        </div>
      </div>
      <div
        role="slider"
        tabIndex={0}
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={1439}
        aria-valuenow={mins}
        aria-valuetext={`${h}:${p2(m)}`}
        className={`touch-none select-none rounded-full outline-none focus-visible:ring-3 focus-visible:ring-ring/50 ${grab ? "cursor-grabbing" : "cursor-grab"}`}
        onKeyDown={(e) => {
          const d = { ArrowRight: 5, ArrowLeft: -5, ArrowUp: 60, ArrowDown: -60 }[e.key];
          if (d) {
            e.preventDefault();
            put(mins + d);
          }
        }}
        onPointerDown={(e) => {
          const { x, y, turn } = turnAt(e);
          // The hand whose tip is nearer; a press elsewhere on the face moves the minutes.
          const hand = Math.hypot(x - hx, y - hy) < Math.hypot(x - mx, y - my) ? "h" : "m";
          setGrab(hand);
          e.currentTarget.setPointerCapture?.(e.pointerId);
          move(hand, turn);
        }}
        onPointerMove={(e) => grab && move(grab, turnAt(e).turn)}
        onPointerUp={() => setGrab(null)}
        onPointerCancel={() => setGrab(null)}
      >
        <svg width={size} height={size} aria-hidden="true" className="block">
          <circle
            cx={c}
            cy={c}
            r={c - 1}
            className={pm ? "fill-muted" : "fill-background stroke-border"}
          />
          {[...Array(60).keys()].map((i) => {
            const big = i % 5 === 0;
            const [x1, y1] = pos(i / 60, c - 6);
            const [x2, y2] = pos(i / 60, c - (big ? 13 : 9));
            return (
              <line
                key={i}
                x1={x1}
                y1={y1}
                x2={x2}
                y2={y2}
                className={big ? "stroke-foreground" : "stroke-muted-foreground/40"}
                strokeWidth={big ? 1.5 : 1}
              />
            );
          })}
          {[12, 3, 6, 9].map((n) => {
            const [x, y] = pos(n / 12, c - 26);
            return (
              <text
                key={n}
                x={x}
                y={y}
                dy="0.35em"
                textAnchor="middle"
                className="fill-muted-foreground text-xs tabular-nums"
              >
                {n}
              </text>
            );
          })}
          <line
            x1={c}
            y1={c}
            x2={hx}
            y2={hy}
            className="stroke-foreground"
            strokeWidth={grab === "h" ? 6 : 5}
            strokeLinecap="round"
          />
          <line
            x1={c}
            y1={c}
            x2={mx}
            y2={my}
            className="stroke-foreground"
            strokeWidth={grab === "m" ? 4 : 3}
            strokeLinecap="round"
          />
          <circle
            cx={c}
            cy={c}
            r={4}
            className="fill-background stroke-foreground"
            strokeWidth={2}
          />
        </svg>
      </div>
    </div>
  );
}
