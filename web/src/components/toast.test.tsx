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
