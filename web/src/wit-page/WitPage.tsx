import { RiArrowDownSLine } from "@remixicon/react";
import { useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { InputGroupButton } from "@/components/ui/input-group";
import { Toaster } from "@/components/ui/sonner";
import {
  WitPage as api,
  events,
  type WitHistory,
  type WitOpenMsg,
  type WitPageState,
} from "@/mygo";
import { makeT, type T } from "@/shared/i18n";
import { forwardShortcuts } from "@/shared/shortcuts";
import { applyTheme, type Theme } from "@/shared/theme";
import { type Decide, Panel, witError } from "@/wit/Panel";
import { useResume } from "@/wit/resume";
import { WitTransport } from "@/wit/transport";

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

/**
 * The Wit page, embedded in the main window's Wit section: the route's ask conversation (0: a new
 * one) and its input. Native owns the sidebar, the title and the no-model state.
 *
 * Listeners first (witOpen, witState, the transport's wit), then load: mygo drops events sent
 * before a listener exists, so Go keeps the route's conversation as a versioned snapshot that load
 * returns and witOpen carries, and only a newer version applies. A switch ends the old stream and
 * follows the new conversation under a new generation (an older follow's or ask's result is
 * ignored); the core run goes on. A first question's conversation is kept for the next one; the
 * witOpen native sends for it (maybe before ask returns) is the conversation shown, no switch.
 */
export function WitPage() {
  const [st, setSt] = useState<WitPageState>();
  const [view, setView] = useState<{ history: WitHistory; tr: WitTransport; key: number }>();
  const [handed, setHanded] = useState<{ text: string }>(); // a question native handed over
  const [mode, setMode] = useState<Theme["mode"]>();
  const witResume = useResume(view?.tr);
  const resetResume = useRef(witResume.reset);
  resetResume.current = witResume.reset;

  useEffect(() => {
    let live = true;
    const conv = { current: -1 }; // shown: none yet
    let version = 0; // the snapshot's applied
    let gen = 0; // the selection
    let creating: Promise<unknown> | undefined; // a first question's ask in flight
    const tr = new WitTransport({
      ask: (text) => {
        const g = gen;
        const p = api.ask(conv.current, text).then((rs) => {
          if (g === gen) conv.current = rs.conversation;
          return rs.run;
        });
        if (conv.current === 0) {
          const c: Promise<unknown> = p
            .catch(() => {})
            .finally(() => {
              if (creating === c) creating = undefined;
            });
          creating = c;
        }
        return p;
      },
      follow: () => api.follow(conv.current),
      stop: (run) => api.stop(run),
    });
    const open = (c: number) => {
      conv.current = c;
      const g = ++gen;
      setView(undefined);
      resetResume.current();
      tr.follow().then(
        (history) => {
          if (live && history && g === gen) setView({ history, tr, key: g });
        },
        (e) => {
          if (live && g === gen) {
            setHanded(undefined); // a question for this conversation is not carried to the next
            toast.error(message(e));
          }
        },
      );
    };
    // The creation's notice waits for the ask (which keeps the conversation); navigation switches
    // at once, and the ask's result is then not shown (a Stop asked for still stops its run).
    const onOpen = (m: Omit<WitOpenMsg, "created"> & { created?: boolean }) => {
      if (m.version <= version) return;
      version = m.version;
      const apply = () => {
        if (live && version === m.version && m.conversation !== conv.current) open(m.conversation);
      };
      if (creating && m.created) void creating.then(apply);
      else apply();
    };
    let pushed = false; // a witState came: newer than Load's state fields
    const offs = [
      events.witOpen.on(onOpen),
      events.witAsk.on((text) => setHanded({ text })),
      events.witState.on((s) => {
        pushed = true;
        setSt(s);
        onOpen(s);
      }),
    ];
    api.load().then(
      (s) => {
        if (!live) return;
        if (!pushed) setSt(s);
        onOpen(s);
      },
      (e) => live && toast.error(message(e)),
    );
    return () => {
      live = false;
      for (const off of offs) off();
      tr.close();
    };
  }, []);

  useEffect(() => forwardShortcuts((dir) => api.navigate(dir)), []);

  const theme = st?.theme as Theme | undefined;
  useEffect(() => {
    if (!theme) return;
    const apply = (t: Theme) => {
      setMode(t.mode);
      return applyTheme(t);
    };
    let stop = apply(theme);
    const off = events.theme.on((t) => {
      stop();
      stop = apply(t as Theme);
    });
    return () => {
      off();
      stop();
    };
  }, [theme]);

  const t = useMemo(() => makeT(st?.text ?? {}), [st?.text]);
  if (!st) return <main />;
  return (
    <main className="h-screen">
      {view && (
        <Conversation
          key={view.key}
          view={view}
          model={st.model}
          resume={witResume.resume}
          decide={witResume.decide}
          handed={handed}
          onAsked={() => setHanded(undefined)}
          t={t}
        />
      )}
      <Toaster theme={mode ?? (st.theme as Theme).mode} />
    </main>
  );
}

function Conversation({
  view,
  model,
  resume,
  decide,
  handed,
  onAsked,
  t,
}: {
  view: { history: WitHistory; tr: WitTransport };
  model: string;
  resume?: { run: number };
  decide: ReturnType<typeof useResume>["decide"];
  handed?: { text: string };
  onAsked(): void;
  t: T;
}) {
  const fail = (e: unknown) => toast.error(witError(message(e), t));
  const onDecide: Decide = async (a, accept, all) => {
    try {
      await decide(a.run, () => (all ? api.decideAll(a.run, accept) : api.decide(a.id, accept)));
      return true;
    } catch (e) {
      fail(e);
      return false;
    }
  };
  return (
    <Panel
      layout="page"
      history={view.history}
      transport={view.tr}
      locked={false}
      resume={resume}
      onDecide={onDecide}
      onLink={(url) => api.openLink(url)}
      onCite={(id) => void api.openCited(id).catch(fail)}
      ask={handed}
      onAsked={onAsked}
      t={t}
      slots={{
        input: (
          <InputGroupButton
            variant="outline"
            size="sm"
            title={t("wit.model")}
            onClick={() => void api.openModels().catch(fail)}
          >
            {model}
            <RiArrowDownSLine />
          </InputGroupButton>
        ),
      }}
    />
  );
}
