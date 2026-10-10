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
