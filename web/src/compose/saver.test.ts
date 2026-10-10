import type { ComposeEdit, SaveResult } from "@/mygo";
import { createSaver } from "./saver";

const ed = (body: string): ComposeEdit => ({
  to: [],
  cc: [],
  bcc: [],
  replyTo: "",
  subject: "",
  body,
  revision: 1,
});
const ok: SaveResult = { revision: 2, conflict: false };

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

test("saves 1 s after the last edit", async () => {
  const save = vi.fn().mockResolvedValue(ok);
  const s = createSaver(save);
  s.edit(ed("a"));
  await vi.advanceTimersByTimeAsync(500);
  s.edit(ed("b"));
  await vi.advanceTimersByTimeAsync(999);
  expect(save).not.toHaveBeenCalled();
  await vi.advanceTimersByTimeAsync(1);
  expect(save).toHaveBeenCalledTimes(1);
  expect(save.mock.calls[0][0].body).toBe("b");
  expect(s.status).toBe("saved");
});

test("waits while composing", async () => {
  const save = vi.fn().mockResolvedValue(ok);
  const s = createSaver(save);
  s.setComposing(true);
  s.edit(ed("a"));
  await vi.advanceTimersByTimeAsync(5000);
  expect(save).not.toHaveBeenCalled();
  s.setComposing(false);
  await vi.advanceTimersByTimeAsync(999);
  expect(save).not.toHaveBeenCalled();
  await vi.advanceTimersByTimeAsync(1);
  expect(save).toHaveBeenCalledTimes(1);
});

test("flushes on demand", async () => {
  const save = vi.fn().mockResolvedValue(ok);
  const s = createSaver(save);
  s.edit(ed("a"));
  await s.flush();
  expect(save).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(5000);
  expect(save).toHaveBeenCalledTimes(1);
});

test("one save in flight", async () => {
  let release!: (r: SaveResult) => void;
  const save = vi
    .fn()
    .mockImplementationOnce(() => new Promise((r) => (release = r)))
    .mockResolvedValue(ok);
  const s = createSaver(save);
  s.edit(ed("a"));
  await vi.advanceTimersByTimeAsync(1000);
  s.edit(ed("b"));
  s.edit(ed("c"));
  await vi.advanceTimersByTimeAsync(3000);
  expect(save).toHaveBeenCalledTimes(1);
  release(ok);
  await vi.advanceTimersByTimeAsync(1000);
  expect(save).toHaveBeenCalledTimes(2);
  expect(save.mock.calls[1][0].body).toBe("c");
});

test("stops on conflict", async () => {
  const save = vi.fn().mockResolvedValue({ revision: 1, conflict: true });
  const s = createSaver(save);
  s.edit(ed("a"));
  await vi.advanceTimersByTimeAsync(1000);
  expect(s.status).toBe("conflict");
  s.edit(ed("b"));
  await vi.advanceTimersByTimeAsync(5000);
  await s.flush();
  expect(save).toHaveBeenCalledTimes(1);
});

test("a failed save stays unsaved and flush returns", async () => {
  const save = vi.fn().mockRejectedValue(new Error("x"));
  const s = createSaver(save);
  s.edit(ed("a"));
  await s.flush();
  expect(s.status).toBe("unsaved");
  expect(save).toHaveBeenCalledTimes(1);
});

test("after a save, an edit made meanwhile waits only the remaining time", async () => {
  let release!: (r: SaveResult) => void;
  const save = vi
    .fn()
    .mockImplementationOnce(() => new Promise((r) => (release = r)))
    .mockResolvedValue(ok);
  const s = createSaver(save);
  s.edit(ed("a"));
  await vi.advanceTimersByTimeAsync(1000);
  s.edit(ed("b"));
  await vi.advanceTimersByTimeAsync(600); // b is now 600 ms old
  release(ok);
  await vi.advanceTimersByTimeAsync(399);
  expect(save).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(1);
  expect(save).toHaveBeenCalledTimes(2);
});

test("after a slow save, an old edit saves at once", async () => {
  let release!: (r: SaveResult) => void;
  const save = vi
    .fn()
    .mockImplementationOnce(() => new Promise((r) => (release = r)))
    .mockResolvedValue(ok);
  const s = createSaver(save);
  s.edit(ed("a"));
  await vi.advanceTimersByTimeAsync(1000);
  s.edit(ed("b"));
  await vi.advanceTimersByTimeAsync(3000);
  release(ok);
  await vi.advanceTimersByTimeAsync(0);
  expect(save).toHaveBeenCalledTimes(2);
});

test("dispose cancels the timer and the post-save reschedule", async () => {
  const save = vi.fn().mockResolvedValue(ok);
  const s = createSaver(save);
  s.edit(ed("a"));
  s.dispose();
  await vi.advanceTimersByTimeAsync(5000);
  expect(save).not.toHaveBeenCalled();

  let release!: (r: SaveResult) => void;
  const slow = vi.fn().mockImplementationOnce(() => new Promise((r) => (release = r)));
  const t = createSaver(slow);
  t.edit(ed("a"));
  await vi.advanceTimersByTimeAsync(1000);
  t.edit(ed("b"));
  t.dispose();
  release(ok);
  await vi.advanceTimersByTimeAsync(5000);
  expect(slow).toHaveBeenCalledTimes(1);
});

const deferred = <T>() => {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
};

test("run waits for the save then chains the revision", async () => {
  const save = vi
    .fn()
    .mockResolvedValueOnce({ revision: 2, conflict: false })
    .mockResolvedValue({ revision: 4, conflict: false });
  const s = createSaver(save, { revision: 1 });
  s.edit(ed("a"));
  const op = vi.fn(async (r: number) => {
    expect(save).toHaveBeenCalledTimes(1);
    return { revision: r + 1, conflict: false };
  });
  const r = await s.run(op);
  expect(save.mock.calls[0][0]).toMatchObject({ body: "a", revision: 1 });
  expect(op.mock.calls[0][0]).toBe(2);
  expect(r).toEqual({ revision: 3, conflict: false });
  s.edit(ed("b"));
  await vi.advanceTimersByTimeAsync(1000);
  expect(save.mock.calls[1][0]).toMatchObject({ body: "b", revision: 3 });
});

test("frozen refuses edits", async () => {
  const save = vi.fn().mockResolvedValue(ok);
  const s = createSaver(save);
  const refused = vi.fn();
  s.onRefused(refused);
  for (const fail of [false, true]) {
    const d = deferred<{ revision: number; conflict: boolean }>();
    const done = s.run(() => d.promise).catch(() => null);
    await vi.advanceTimersByTimeAsync(0);
    expect(s.frozen).toBe(true);
    expect(s.edit(ed("x"))).toBe(false);
    expect(refused).toHaveBeenCalledTimes(fail ? 2 : 1);
    if (fail) d.reject(new Error("x"));
    else d.resolve(ok);
    await done;
    expect(s.frozen).toBe(false);
    s.edit(ed("y"));
    await vi.advanceTimersByTimeAsync(1000);
    expect(save).toHaveBeenCalledTimes(fail ? 2 : 1);
  }
});

test("flush false stops run", async () => {
  const save = vi.fn().mockRejectedValue(new Error("x"));
  const s = createSaver(save);
  s.edit(ed("a"));
  const op = vi.fn();
  expect(await s.run(op)).toBeNull();
  expect(op).not.toHaveBeenCalled();
  expect(s.frozen).toBe(false);

  const t = createSaver(vi.fn().mockResolvedValue(ok));
  t.setSettle(async () => false); // the composition never ended
  t.edit(ed("a"));
  expect(await t.flush()).toBe(false);
  expect(await t.run(op)).toBeNull();
  expect(op).not.toHaveBeenCalled();
});

test("flush settles first: input made while settling is saved", async () => {
  const save = vi.fn().mockResolvedValue(ok);
  const s = createSaver(save);
  s.setSettle(async () => {
    s.edit(ed("committed"));
    return true;
  });
  expect(await s.flush()).toBe(true);
  expect(save).toHaveBeenCalledTimes(1);
  expect(save.mock.calls[0][0].body).toBe("committed");
});

test("one op at a time", async () => {
  const s = createSaver(vi.fn().mockResolvedValue(ok));
  const log: string[] = [];
  const op = (name: string) => async (r: number) => {
    log.push(`${name} start`);
    await new Promise((res) => setTimeout(res, 100));
    log.push(`${name} end`);
    return { revision: r, conflict: false };
  };
  const all = Promise.all([
    s.run(op("a")),
    s.flush(async () => {
      log.push("close start");
      await new Promise((res) => setTimeout(res, 100));
      log.push("close end");
    }),
    s.run(op("b")),
  ]);
  await vi.advanceTimersByTimeAsync(1000);
  await all;
  expect(log).toEqual(["a start", "a end", "close start", "close end", "b start", "b end"]);
});

test("use latest drops old content", async () => {
  const late = deferred<SaveResult>();
  const save = vi
    .fn()
    .mockResolvedValueOnce({ revision: 1, conflict: true })
    .mockImplementationOnce(() => late.promise)
    .mockResolvedValue(ok);
  const s = createSaver(save, { revision: 1 });
  s.edit(ed("old"));
  await vi.advanceTimersByTimeAsync(1000);
  expect(s.status).toBe("conflict");
  s.edit(ed("older")); // typed during the conflict
  const kept = s.keepMine(5); // its response comes too late
  await vi.advanceTimersByTimeAsync(0);
  s.useLatest(9, ed("latest"));
  expect(s.status).toBe("saved");
  late.resolve({ revision: 6, conflict: false });
  await kept;
  await vi.advanceTimersByTimeAsync(5000);
  expect(save).toHaveBeenCalledTimes(2);
  expect(s.status).toBe("saved");
  s.edit(ed("new"));
  await vi.advanceTimersByTimeAsync(1000);
  expect(save).toHaveBeenCalledTimes(3);
  expect(save.mock.calls[2][0]).toMatchObject({ body: "new", revision: 9 });
});

test("use latest drops a pending edit", async () => {
  const save = vi.fn().mockResolvedValue({ revision: 1, conflict: true });
  const s = createSaver(save, { revision: 1 });
  s.edit(ed("a"));
  await vi.advanceTimersByTimeAsync(1000);
  s.edit(ed("b"));
  s.useLatest(9, ed("latest"));
  await vi.advanceTimersByTimeAsync(5000);
  expect(await s.flush()).toBe(true);
  expect(save).toHaveBeenCalledTimes(1);
});

test("run conflict stops saves", async () => {
  const save = vi.fn().mockResolvedValue({ revision: 6, conflict: false });
  const s = createSaver(save, { revision: 1 });
  const r = await s.run(async () => ({ revision: 1, conflict: true }));
  expect(r?.conflict).toBe(true);
  expect(s.status).toBe("conflict");
  s.edit(ed("mine"));
  await vi.advanceTimersByTimeAsync(5000);
  expect(save).not.toHaveBeenCalled();
  expect(await s.keepMine(5)).toBe(true);
  expect(save.mock.calls[0][0]).toMatchObject({ body: "mine", revision: 5 });
  expect(s.status).toBe("saved");
  s.edit(ed("more"));
  await vi.advanceTimersByTimeAsync(1000);
  expect(save.mock.calls[1][0]).toMatchObject({ body: "more", revision: 6 });
});

test("a settling that does not finish saves nothing; the close answer still goes out", async () => {
  const save = vi.fn().mockResolvedValue(ok);
  const s = createSaver(save);
  s.setComposing(true);
  s.edit(ed("ni"));
  s.setSettle(async () => false);
  const after = vi.fn();
  expect(await s.flush(after)).toBe(false);
  expect(after).toHaveBeenCalledWith(false);
  expect(save).not.toHaveBeenCalled();
  expect(await s.keepMine(3)).toBe(false);
  expect(save).not.toHaveBeenCalled();
});

test("never saves while composing, even when flushed", async () => {
  const save = vi.fn().mockResolvedValue(ok);
  const s = createSaver(save);
  s.edit(ed("a"));
  s.setComposing(true);
  await vi.advanceTimersByTimeAsync(5000);
  expect(await s.flush()).toBe(false); // the settle hook claims done, the composition is still open
  expect(save).not.toHaveBeenCalled();
  s.setComposing(false);
  expect(await s.flush()).toBe(true);
  expect(save).toHaveBeenCalledTimes(1);
});

test("an autosave due during an op waits for it", async () => {
  const save = vi.fn().mockResolvedValue(ok);
  const s = createSaver(save);
  s.edit(ed("a")); // due at 1000
  const d = deferred<{ revision: number; conflict: boolean }>();
  let started = false;
  const op = s.run(async () => {
    started = true;
    return d.promise;
  });
  await vi.advanceTimersByTimeAsync(0);
  expect(started).toBe(true);
  expect(save).toHaveBeenCalledTimes(1); // the flush took it
  await vi.advanceTimersByTimeAsync(5000);
  expect(save).toHaveBeenCalledTimes(1);
  d.resolve(ok);
  await op;
});

test("keep mine settles, freezes and saves the full snapshot", async () => {
  let frozenDuringSave = false;
  const save = vi
    .fn()
    .mockResolvedValueOnce({ revision: 1, conflict: true })
    .mockImplementation(async () => {
      frozenDuringSave = s.frozen;
      return ok;
    });
  const s = createSaver(save, { revision: 1, snapshot: ed("loaded") });
  s.edit(ed("a"));
  await vi.advanceTimersByTimeAsync(1000);
  expect(s.status).toBe("conflict");
  s.setSettle(async () => {
    s.edit(ed("a, committed"));
    return true;
  });
  expect(await s.keepMine(5)).toBe(true);
  expect(save.mock.calls[1][0]).toMatchObject({ body: "a, committed", revision: 5 });
  expect(frozenDuringSave).toBe(true);
  expect(s.frozen).toBe(false);
});

test("keep mine after use latest saves the latest content, not the abandoned edit", async () => {
  const save = vi.fn().mockResolvedValueOnce({ revision: 1, conflict: true }).mockResolvedValue(ok);
  const s = createSaver(save, { revision: 1 });
  s.edit(ed("abandoned"));
  await vi.advanceTimersByTimeAsync(1000);
  s.useLatest(9, ed("latest"));
  expect(await s.keepMine(9)).toBe(true);
  expect(save.mock.calls[1][0]).toMatchObject({ body: "latest", revision: 9 });
});

test("keep mine with no edit saves the loaded snapshot", async () => {
  const save = vi.fn().mockResolvedValue(ok);
  const s = createSaver(save, { revision: 1, snapshot: ed("loaded") });
  await s.run(async () => ({ revision: 1, conflict: true }));
  expect(await s.keepMine(4)).toBe(true);
  expect(save.mock.calls[0][0]).toMatchObject({ body: "loaded", revision: 4 });
});

test("an op flushing through its own flush does not deadlock", async () => {
  const save = vi.fn().mockResolvedValue(ok);
  const s = createSaver(save);
  const r = await s.run(async (rev, flush) => {
    expect(await flush()).toBe(true);
    return { revision: rev, conflict: false };
  });
  expect(r).toEqual({ revision: 0, conflict: false });
});

test("a queued autosave rechecks the delay against the newest edit", async () => {
  const slow = deferred<SaveResult>();
  const save = vi
    .fn()
    .mockImplementationOnce(() => slow.promise)
    .mockResolvedValue(ok);
  const s = createSaver(save);
  s.edit(ed("a"));
  await vi.advanceTimersByTimeAsync(1000); // a in flight
  await vi.advanceTimersByTimeAsync(100);
  s.edit(ed("b")); // its timer fires at 2100 and queues behind a
  await vi.advanceTimersByTimeAsync(1000);
  await vi.advanceTimersByTimeAsync(50);
  s.edit(ed("c")); // at 2150: due at 3150
  await vi.advanceTimersByTimeAsync(50);
  slow.resolve(ok); // the queued job runs at 2200
  await vi.advanceTimersByTimeAsync(949);
  expect(save).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(1);
  expect(save).toHaveBeenCalledTimes(2);
  expect(save.mock.calls[1][0].body).toBe("c");
});
