import { wheelForwarder } from "./wheel";

const wheel = (deltaY: number, deltaX = 0) =>
  new WheelEvent("wheel", { deltaY, deltaX, cancelable: true });

test("wheel coalesces per frame: three wheels in one frame → one wheel(sum)", () => {
  const frames: FrameRequestCallback[] = [];
  const send = vi.fn();
  const on = wheelForwarder(send, (f) => frames.push(f));
  const evs = [wheel(10), wheel(20, 3), wheel(-5)];
  for (const e of evs) on(e);
  expect(evs.every((e) => e.defaultPrevented)).toBe(true);
  expect(frames).toHaveLength(1);
  expect(send).not.toHaveBeenCalled();
  frames[0](0);
  expect(send).toHaveBeenCalledExactlyOnceWith(25);
  on(wheel(7));
  frames[1](0);
  expect(send).toHaveBeenLastCalledWith(7);
});

test("horizontal wheels stay in the body", () => {
  const frames: FrameRequestCallback[] = [];
  const send = vi.fn();
  const on = wheelForwarder(send, (f) => frames.push(f));
  const e = wheel(2, 30);
  on(e);
  expect(e.defaultPrevented).toBe(false);
  expect(frames).toHaveLength(0);
});

test("line-mode deltas become pixels", () => {
  const frames: FrameRequestCallback[] = [];
  const send = vi.fn();
  const on = wheelForwarder(send, (f) => frames.push(f));
  on(new WheelEvent("wheel", { deltaY: 3, deltaMode: 1, cancelable: true }));
  frames[0](0);
  expect(send).toHaveBeenCalledWith(48);
});
