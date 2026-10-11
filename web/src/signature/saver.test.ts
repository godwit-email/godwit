import { type Doc, signatureSaver } from "./saver";

function setup() {
  const loads: { id: number; resolve(html: string): void }[] = [];
  const api = {
    load: vi.fn(
      (id: number) =>
        new Promise<string>((resolve) => {
          loads.push({ id, resolve });
        }),
    ),
    save: vi.fn(async (_id: number, _html: string) => {}),
    saved: vi.fn(async (_tok: number, _ok: boolean) => {}),
  };
  const docs: (Doc | undefined)[] = [];
  const s = signatureSaver(api, (d) => docs.push(d));
  return { api, loads, docs, s };
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("signatureSaver", () => {
  it("saves once 600 ms after the last edit", async () => {
    const { api, s } = setup();
    s.show(1);
    s.edit(1, "<p>a</p>");
    await vi.advanceTimersByTimeAsync(300);
    s.edit(1, "<p>ab</p>");
    await vi.advanceTimersByTimeAsync(599);
    expect(api.save).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(api.save.mock.calls).toEqual([[1, "<p>ab</p>"]]);
  });

  it("an account event saves the shown account's edit as that account first", async () => {
    const { api, s } = setup();
    s.show(1);
    await vi.advanceTimersByTimeAsync(0);
    s.edit(1, "<p>a</p>");
    s.show(2);
    s.edit(1, "<p>late</p>"); // the old editor, still mounted: dropped
    await vi.advanceTimersByTimeAsync(600);
    expect(api.save.mock.calls).toEqual([[1, "<p>a</p>"]]);
    expect(api.save.mock.invocationCallOrder[0]).toBeLessThan(api.load.mock.invocationCallOrder[1]);
    expect(api.load.mock.calls.map((c) => c[0])).toEqual([1, 2]);
  });

  it("ignores a Load older than the newest account", async () => {
    const { loads, docs, s } = setup();
    s.show(1);
    await vi.advanceTimersByTimeAsync(0);
    s.show(2);
    await vi.advanceTimersByTimeAsync(0);
    loads[1].resolve("<p>two</p>");
    loads[0].resolve("<p>one</p>");
    await vi.runAllTimersAsync();
    expect(docs.filter(Boolean)).toEqual([{ account: 2, html: "<p>two</p>" }]);
  });

  it("a flush saves at once and answers with its token", async () => {
    const { api, s } = setup();
    s.show(1);
    s.edit(1, "<p>a</p>");
    s.flush(7);
    await vi.advanceTimersByTimeAsync(0);
    expect(api.save.mock.calls).toEqual([[1, "<p>a</p>"]]);
    expect(api.saved).toHaveBeenCalledWith(7, true);
    api.save.mockRejectedValueOnce(new Error("disk"));
    s.edit(1, "<p>b</p>");
    s.flush(8);
    await vi.advanceTimersByTimeAsync(0);
    expect(api.saved).toHaveBeenLastCalledWith(8, false);
    await vi.advanceTimersByTimeAsync(600);
    expect(api.save).toHaveBeenCalledTimes(2);
    s.flush(9); // the failed edit is saved again
    await vi.advanceTimersByTimeAsync(0);
    expect(api.save).toHaveBeenLastCalledWith(1, "<p>b</p>");
    expect(api.saved).toHaveBeenLastCalledWith(9, true);
  });

  it("a failed debounced save is retried by the next flush, unless a newer edit replaced it", async () => {
    const { api, s } = setup();
    s.show(1);
    await vi.advanceTimersByTimeAsync(0);
    api.save.mockRejectedValueOnce(new Error("disk"));
    s.edit(1, "<p>a</p>");
    await vi.advanceTimersByTimeAsync(600);
    s.flush(1);
    await vi.advanceTimersByTimeAsync(0);
    expect(api.save.mock.calls).toEqual([
      [1, "<p>a</p>"],
      [1, "<p>a</p>"],
    ]);
    expect(api.saved).toHaveBeenLastCalledWith(1, true);

    let fail: (e: Error) => void = () => {};
    api.save.mockImplementationOnce(() => new Promise((_, rej) => (fail = rej)));
    s.edit(1, "<p>b</p>");
    await vi.advanceTimersByTimeAsync(600);
    s.edit(1, "<p>c</p>"); // newer, while b's save is in flight
    fail(new Error("disk"));
    s.flush(2);
    await vi.advanceTimersByTimeAsync(0);
    expect(api.save).toHaveBeenLastCalledWith(1, "<p>c</p>");
    expect(api.save).toHaveBeenCalledTimes(4);
  });

  it("a failed save for A survives switching to B and typing there; a flush saves both", async () => {
    const { api, s } = setup();
    s.show(1);
    await vi.advanceTimersByTimeAsync(0);
    api.save.mockRejectedValueOnce(new Error("disk")).mockRejectedValueOnce(new Error("disk"));
    s.edit(1, "<p>a</p>");
    await vi.advanceTimersByTimeAsync(600);
    s.show(2); // its save of A fails again
    await vi.advanceTimersByTimeAsync(0);
    expect(api.save).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(0);
    s.edit(2, "<p>b</p>");
    s.flush(5);
    await vi.advanceTimersByTimeAsync(0);
    expect(api.save.mock.calls.slice(2)).toEqual(
      expect.arrayContaining([
        [1, "<p>a</p>"],
        [2, "<p>b</p>"],
      ]),
    );
    expect(api.saved).toHaveBeenLastCalledWith(5, true);
  });
});
