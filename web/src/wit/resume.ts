import { useCallback, useEffect, useRef, useState } from "react";
import type { WitTransport } from "./transport";

/**
 * The decide → resume protocol, for Panel's `resume`: `decide(run, op)` notes the run before op
 * decides (its events may come before op returns: the transport keeps them), and the run's next
 * event of the conversation followed resumes its stream. An answer already over by then replays
 * whole; a decision that leaves others of the run pending resumes nothing until the run goes on.
 * `reset` forgets the last resume (a panel remounted on a new history).
 */
export function useResume(tr: WitTransport | undefined) {
  const [resume, setResume] = useState<{ run: number }>();
  const decided = useRef(0);
  useEffect(
    () =>
      tr?.watch((ev) => {
        if (!decided.current || ev.run !== decided.current) return;
        decided.current = 0;
        setResume({ run: ev.run });
      }),
    [tr],
  );
  const decide = useCallback(<R>(run: number, op: () => Promise<R>) => {
    decided.current = run;
    return op();
  }, []);
  const reset = useCallback(() => setResume(undefined), []);
  return { resume, decide, reset };
}
