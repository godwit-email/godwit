import { render } from "@testing-library/react";
import { useRef } from "react";
import { expect, test, vi } from "vitest";
import { NativeToasts, toast } from "./toast";

const bridge = () => ({ toast: vi.fn(async () => {}), toastBar: vi.fn(async () => {}) });

test("a page's toasts go to native by its bridge, with their kind", () => {
  const api = bridge();
  const { unmount } = render(<NativeToasts api={api} />);
  toast("Saved");
  toast.error("Failed");
  toast.warning("Careful");
  expect(api.toast.mock.calls).toEqual([
    ["", "Saved"],
    ["error", "Failed"],
    ["warning", "Careful"],
  ]);
  unmount();
  toast("After");
  expect(api.toast).toHaveBeenCalledTimes(3); // no page, no bridge
});

function Page({ api }: { api: ReturnType<typeof bridge> }) {
  const bar = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLDivElement>(null);
  return (
    <div ref={bar}>
      <div ref={input} />
      <NativeToasts api={api} bar={bar} input={input} />
    </div>
  );
}

test("native hears where the bar and its input are, once they mount", () => {
  const api = bridge();
  const box = { left: 10, top: 500, width: 600, height: 60, right: 610, bottom: 560 } as DOMRect;
  const spy = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue(box);
  render(<Page api={api} />);
  expect(api.toastBar).toHaveBeenLastCalledWith(
    { X: 10, Y: 500, W: 600, H: 60 },
    { X: 10, Y: 500, W: 600, H: 60 },
  );
  spy.mockRestore();
});

// As WitPage: the dock's refs outlive the keyed Conversation that attaches them.
function Remounting({ api, k }: { api: ReturnType<typeof bridge>; k: number }) {
  const dock = useRef({
    bar: { current: null as HTMLDivElement | null },
    input: { current: null as HTMLDivElement | null },
  });
  return (
    <>
      <div key={k} ref={dock.current.bar} data-w={String(100 * k)}>
        <div ref={dock.current.input} data-w={String(100 * k)} />
      </div>
      <NativeToasts api={api} bar={dock.current.bar} input={dock.current.input} />
    </>
  );
}

test("native follows the bar and input to a remounted conversation", () => {
  const api = bridge();
  const spy = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement,
  ) {
    const w = Number(this.dataset.w ?? 0);
    return { left: 0, top: 500, width: w, height: 60, right: w, bottom: 560 } as DOMRect;
  });
  const { rerender } = render(<Remounting api={api} k={1} />);
  expect(api.toastBar).toHaveBeenLastCalledWith(
    { X: 0, Y: 500, W: 100, H: 60 },
    { X: 0, Y: 500, W: 100, H: 60 },
  );
  rerender(<Remounting api={api} k={2} />);
  expect(api.toastBar).toHaveBeenLastCalledWith(
    { X: 0, Y: 500, W: 200, H: 60 },
    { X: 0, Y: 500, W: 200, H: 60 },
  );
  spy.mockRestore();
});
