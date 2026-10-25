import type {
  Approval,
  Citation,
  MailHit,
  WitCall,
  WitHistory,
  WitResult,
  WitStep,
  WitTurn,
} from "@/mygo";

// Test fixtures only.

/** A message of a history fixture: a question, or the answer to the one before it. */
export type Shown = {
  id: string;
  role: string;
  text: string;
  reasoning: string;
  quoted: MailHit[];
  calls: WitCall[];
  results: WitResult[];
  citations: Citation[];
  runs: number[];
  elapsed: number;
};

/** A history fixture written as messages and approvals (legacy). */
export type Legacy = Partial<WitHistory> & { messages?: Shown[]; approvals?: Approval[] };

/** A fixture turn's steps as one message gives them: its reasoning, its calls, then its text. */
export const stepsOf = (t: Pick<WitTurn, "reasoning" | "calls" | "text">): WitStep[] => [
  ...(t.reasoning ? [{ kind: "reasoning", text: t.reasoning, call: 0 }] : []),
  ...t.calls.map((_, i) => ({ kind: "call", text: "", call: i })),
  ...(t.text ? [{ kind: "text", text: t.text, call: 0 }] : []),
];

const none: WitHistory = {
  conversation: 1,
  model: "",
  effort: "",
  usage: { input: 0, output: 0, context: 0, history: 0, summary: 0, compactable: false },
  running: 0,
  turns: [],
};

/**
 * A history written as messages (a question, then its answer) and approvals, as the turns core
 * gives: a question "q<n>" is turn "n"; each call with its result and its run's approval, the
 * other approvals the turn's own; none asked again while a call waits or a run runs.
 */
export const legacy = (h: Legacy): WitHistory => {
  const { messages = [], approvals = [], ...rest } = h;
  // copies: a fixture spread into another keeps its own turns
  const turns: WitTurn[] = (rest.turns ?? []).map((t) => ({ ...t, approvals: [...t.approvals] }));
  for (const m of messages) {
    if (m.role === "user" || turns.length === 0) {
      turns.push({
        id: m.role === "user" ? m.id.replace(/^q/, "") : m.id,
        question: m.role === "user" ? m.text : "",
        command: null,
        quoted: m.role === "user" ? m.quoted : [],
        text: "",
        reasoning: "",
        calls: [],
        steps: [],
        approvals: [],
        citations: [],
        runs: [...m.runs],
        elapsed: 0,
        canAskAgain: false,
      });
      if (m.role === "user") continue;
    }
    const t = turns[turns.length - 1];
    t.text = m.text;
    t.reasoning = m.reasoning;
    t.citations = m.citations;
    t.elapsed = m.elapsed;
    t.runs = [...new Set([...t.runs, ...m.runs])];
    t.calls = m.calls.map((c) => ({
      call: c,
      result: m.results.find((r) => r.callId === c.id) ?? null,
      approval: approvals.find((a) => a.callId === c.id && m.runs.includes(a.run)) ?? null,
    }));
    t.steps = stepsOf(t);
  }
  for (const a of approvals) {
    if (turns.some((t) => t.calls.some((c) => c.approval === a))) continue;
    turns.find((t) => t.runs.includes(a.run))?.approvals.push(a);
  }
  const waits = approvals.some((a) => a.state === "pending") || !!rest.running;
  for (const t of turns) t.canAskAgain = !waits && !t.command;
  return { ...none, ...rest, turns };
};
