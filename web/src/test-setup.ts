import "@testing-library/jest-dom/vitest";

// jsdom has no ResizeObserver; react-resizable-panels needs one. A test that
// drives one (reader/Body) stubs its own over this.
if (!("ResizeObserver" in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
}

// jsdom lays nothing out, so every element is 0×0 at the origin and
// react-resizable-panels takes each press for a press on its grip, swallowing
// clicks and typing. Tests get plain boxes in its place.
vi.mock("react-resizable-panels", async () => {
  const { createElement } = await import("react");
  type Props = { children?: unknown; className?: string; id?: string; "aria-label"?: string };
  const box =
    (attrs: Record<string, unknown>) =>
    ({ children, className, id, "aria-label": label }: Props) =>
      createElement("div", { ...attrs, className, id, "aria-label": label }, children as never);
  return {
    Group: box({ "data-group": true }),
    Panel: box({ "data-panel": true }),
    Separator: box({ role: "separator" }),
    useDefaultLayout: () => ({ defaultLayout: undefined, onLayoutChanged: () => {} }),
  };
});
