import { currentWindow } from "mygo-runtime";
import { useEffect, useMemo, useRef, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { ComposePage as api, type ComposeState, events } from "@/mygo";
import { applyTheme, type Theme } from "@/shared/theme";
import { createSaver, type Saver } from "./saver";

const list = (s: string) =>
  s
    .split(/[,，;；]/)
    .map((x) => x.trim())
    .filter(Boolean);

/** The compose page: loads the window's draft, autosaves it, flushes when the window closes. */
export function ComposePage() {
  const [state, setState] = useState<ComposeState>();
  const saver = useRef<Saver>(undefined);

  useEffect(() => {
    api.load().then(setState);
    // Closing before the draft loaded has nothing to flush.
    return events.closing.on(async (gen) => {
      await saver.current?.flush();
      await api.closed(gen, !saver.current || saver.current.status === "saved");
    });
  }, []);

  useEffect(() => {
    if (!state) return;
    let stop = applyTheme(state.theme as Theme);
    const off = events.theme.on((t) => {
      stop();
      stop = applyTheme(t as Theme);
    });
    return () => (off(), stop());
  }, [state]);

  if (!state) return <main aria-label="撰写" />;
  return <Editor state={state} saver={saver} />;
}

function Editor({ state, saver }: { state: ComposeState; saver: { current: Saver | undefined } }) {
  const [f, setF] = useState({
    to: state.to.join(", "),
    cc: state.cc.join(", "),
    bcc: state.bcc.join(", "),
    subject: state.subject,
    body: state.body,
  });
  const [showQuote, setShowQuote] = useState(false);
  const [, rerender] = useState(0);
  const [savedAt, setSavedAt] = useState<Date>();

  const s = useMemo(() => {
    let revision = state.revision;
    return createSaver(async (e) => {
      const r = await api.save({ ...e, revision });
      if (!r.conflict) revision = r.revision;
      return r;
    });
  }, [state.revision]);
  saver.current = s;
  useEffect(
    () =>
      s.onChange(() => {
        if (s.status === "saved") setSavedAt(new Date());
        rerender((n) => n + 1);
      }),
    [s],
  );
  useEffect(() => {
    currentWindow.setTitle(f.subject.trim() || "新邮件");
  }, [f.subject]);

  const set = (k: keyof typeof f) => (ev: { target: { value: string } }) => {
    const n = { ...f, [k]: ev.target.value };
    setF(n);
    s.edit({
      to: list(n.to),
      cc: list(n.cc),
      bcc: list(n.bcc),
      replyTo: state.replyTo,
      subject: n.subject,
      body: n.body,
      revision: state.revision,
    });
  };
  const hhmm = savedAt?.toLocaleTimeString("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
  const status =
    s.status === "saving"
      ? "正在保存…"
      : s.status === "saved" && hhmm
        ? `草稿已保存 · ${hhmm}`
        : "未保存";

  return (
    <main
      aria-label="撰写"
      className="flex min-h-screen flex-col gap-3 p-4"
      onCompositionStart={() => s.setComposing(true)}
      onCompositionEnd={() => s.setComposing(false)}
    >
      {s.status === "conflict" && (
        <Alert variant="destructive">草稿已在别处修改，本窗口不再保存</Alert>
      )}
      {(
        [
          ["to", "收件人"],
          ["cc", "抄送"],
          ["bcc", "密送"],
          ["subject", "主题"],
        ] as const
      ).map(([k, label]) => (
        <div key={k} className="flex items-center gap-2">
          <Label htmlFor={k} className="w-16 shrink-0">
            {label}
          </Label>
          <Input id={k} value={f[k]} onChange={set(k)} />
        </div>
      ))}
      <Label htmlFor="body" className="sr-only">
        正文
      </Label>
      <Textarea id="body" className="min-h-64 flex-1" value={f.body} onChange={set("body")} />
      {state.quote && (
        <div>
          <Button variant="ghost" size="sm" onClick={() => setShowQuote(!showQuote)}>
            {showQuote ? "隐藏引用的原邮件" : "显示引用的原邮件"}
          </Button>
          {showQuote && (
            <pre className="whitespace-pre-wrap text-sm text-muted-foreground">{state.quote}</pre>
          )}
        </div>
      )}
      <p className="text-sm text-muted-foreground">{status}</p>
    </main>
  );
}
