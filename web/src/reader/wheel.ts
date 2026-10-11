const LINE = 16; // px per line, for line-mode wheels

/**
 * A wheel listener for the body's windows (passive:false): a mostly vertical wheel is the
 * reader's, as the body is fitted to its content. It is prevented and its deltas summed, sent once
 * per animation frame. Mostly horizontal ones (and pinches) are left to the body.
 */
export function wheelForwarder(
  send: (dy: number) => void,
  raf: (f: FrameRequestCallback) => unknown = requestAnimationFrame,
): (e: WheelEvent) => void {
  let pending = 0;
  let queued = false;
  return (e) => {
    if (e.ctrlKey || Math.abs(e.deltaY) <= Math.abs(e.deltaX)) return;
    e.preventDefault();
    const unit = e.deltaMode === 1 ? LINE : e.deltaMode === 2 ? innerHeight : 1;
    pending += e.deltaY * unit;
    if (queued) return;
    queued = true;
    raf(() => {
      queued = false;
      const dy = pending;
      pending = 0;
      if (dy) send(dy);
    });
  };
}
