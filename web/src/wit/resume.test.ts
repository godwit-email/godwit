import { act, renderHook } from "@testing-library/react";
import type { WitEvent } from "@/mygo";
import { useResume } from "./resume";
import { WitTransport } from "./transport";

vi.mock("@/mygo", () => ({ events: { wit: { on: () => () => {} } } }));

const ev = (e: Partial<WitEvent>): WitEvent => ({
  conversation: 1,
  run: 7,
  kind: "text",
  text: "",
  call: null,
  result: null,
  interrupts: [],
  citations: [],
  approvals: [],
  error: "",
  ...e,
});

async function setup() {
  let listener: ((e: WitEvent) => void) | undefined;
  const api = {
    ask: vi.fn(),
    follow: vi.fn().mockResolvedValue({ conversation: 1, running: 0, messages: [], approvals: [] }),
    stop: vi.fn(),
  };
  const tr = new WitTransport(api, (f) => {
    listener = f;
    return () => {};
  });
  await tr.follow();
  const hook = renderHook(() => useResume(tr));
  const emit = (e: Partial<WitEvent>) => act(() => listener?.(ev(e)));
  return { hook, emit };
}

describe("decide then resume", () => {
  it("the decided run's next event resumes it, once", async () => {
    const { hook, emit } = await setup();
    emit({ run: 7, kind: "text", text: "before" }); // not decided yet: nothing
    expect(hook.result.current.resume).toBeUndefined();
    await act(() => hook.result.current.decide(7, async () => {}));
    emit({ run: 8, kind: "text" }); // another run's
    expect(hook.result.current.resume).toBeUndefined();
    emit({ run: 7, kind: "text", text: "after" });
    const first = hook.result.current.resume;
    expect(first).toEqual({ run: 7 });
    emit({ run: 7, kind: "done" });
    expect(hook.result.current.resume).toBe(first);
  });

  it("the answer already done before decide returned still resumes", async () => {
    const { hook, emit } = await setup();
    let p!: Promise<void>;
    act(() => {
      p = hook.result.current.decide(7, async () => {
        emit({ run: 7, kind: "text", text: "x" });
        emit({ run: 7, kind: "done" });
      });
    });
    await act(() => p);
    expect(hook.result.current.resume).toEqual({ run: 7 });
  });

  it("other approvals still pending: nothing resumes until the run goes on", async () => {
    const { hook, emit } = await setup();
    await act(() => hook.result.current.decide(7, async () => {}));
    expect(hook.result.current.resume).toBeUndefined();
    await act(() => hook.result.current.decide(7, async () => {})); // the last one
    emit({ run: 7, kind: "text", text: "on" });
    expect(hook.result.current.resume).toEqual({ run: 7 });
    act(() => hook.result.current.reset());
    expect(hook.result.current.resume).toBeUndefined();
  });

  it("a failed decision throws to the host", async () => {
    const { hook } = await setup();
    await expect(
      hook.result.current.decide(7, () => Promise.reject(new Error("error.stale_approval"))),
    ).rejects.toThrow("error.stale_approval");
  });
});
