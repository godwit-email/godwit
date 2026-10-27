import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
import type { Approval, WitInfo } from "@/mygo";
import { makeT } from "@/shared/i18n";
import { legacy as hist } from "./fixtures";
import { Panel } from "./Panel";
import { WitTransport } from "./transport";

afterEach(cleanup);

const info: WitInfo = {
  enabled: true,
  translateOnly: false,
  languages: [],
  skills: [],
  chips: [],
  model: "",
};

describe("BatchApprovalCard & Draft Editing", () => {
  const t = makeT("de");

  const setupPanel = async (approval: Approval, onDecide = vi.fn().mockResolvedValue(true)) => {
    const history = hist({
      messages: [
        {
          id: "q0",
          role: "user",
          text: "triage",
          reasoning: "",
          quoted: [],
          calls: [],
          results: [],
          citations: [],
          runs: [10],
          elapsed: 0,
        },
        {
          id: "a1",
          role: "assistant",
          text: "",
          reasoning: "",
          quoted: [],
          calls: [{ id: approval.callId, name: approval.tool, args: approval.args }],
          results: [],
          citations: [],
          runs: [10],
          elapsed: 0,
        },
      ],
      approvals: [approval],
    });

    const api = {
      ask: vi.fn(),
      skill: vi.fn(),
      follow: vi.fn().mockResolvedValue(history),
      stop: vi.fn(),
      compact: vi.fn(),
      rewinds: true,
    };
    const tr = new WitTransport(api, () => () => {});
    await tr.follow();

    render(
      <Panel
        info={info}
        history={history}
        locked={false}
        onDecide={onDecide}
        onLink={vi.fn().mockResolvedValue(undefined)}
        transport={tr}
        t={t}
      />,
    );

    return { user: userEvent.setup(), onDecide };
  };

  test("renders batch items with checkboxes and allows deselecting", async () => {
    const approval: Approval = {
      id: 42,
      run: 10,
      callId: "call_batch_1",
      tool: "move_to_trash",
      args: JSON.stringify({
        action: "move_to_trash",
        ids: [1, 2],
        items: [
          { id: 1, msg_id: 101, subject: "Newsletter A", from: "a@example.com", date: 1700000000 },
          { id: 2, msg_id: 102, subject: "Newsletter B", from: "b@example.com", date: 1700000010 },
        ],
        selected_ids: [1, 2],
      }),
      summary: "Move 2 messages to Trash",
      state: "pending",
      destinations: [],
      error: "",
      sessionAllow: false,
      draft: 0,
      draftEdit: false,
    };

    const { user, onDecide } = await setupPanel(approval);

    expect(screen.getByText("Newsletter A")).toBeInTheDocument();
    expect(screen.getByText("Newsletter B")).toBeInTheDocument();
    expect(screen.getByText("a@example.com")).toBeInTheDocument();
    expect(screen.getByText("b@example.com")).toBeInTheDocument();

    const checkboxes = screen.getAllByRole("checkbox");
    expect(checkboxes).toHaveLength(3); // 1 select-all + 2 items
    expect(checkboxes[1]).toBeChecked();
    expect(checkboxes[2]).toBeChecked();

    // Button shows action and count
    const approveBtn = screen.getByRole("button", { name: /In den Papierkorb \(2\)/ });
    expect(approveBtn).toBeInTheDocument();

    // Deselect item 2
    await user.click(checkboxes[2]);
    expect(checkboxes[2]).not.toBeChecked();

    // Button updates to (1)
    const updatedBtn = screen.getByRole("button", { name: /In den Papierkorb \(1\)/ });
    expect(updatedBtn).toBeInTheDocument();

    // Click Approve (1)
    await user.click(updatedBtn);

    expect(onDecide).toHaveBeenCalledTimes(1);
    const passedArgs = JSON.parse(onDecide.mock.calls[0][3] as string);
    expect(passedArgs.action).toBe("move_to_trash");
    expect(passedArgs.ids).toEqual([1]);
    expect(passedArgs.items).toHaveLength(1);
    expect(passedArgs.items[0].id).toBe(1);
  });

  test("allows switching action from dropdown", async () => {
    const approval: Approval = {
      id: 43,
      run: 10,
      callId: "call_batch_2",
      tool: "move_to_trash",
      args: JSON.stringify({
        action: "move_to_trash",
        ids: [1],
        items: [
          {
            id: 1,
            msg_id: 101,
            subject: "Keep this invoice",
            from: "billing@x.com",
            date: 1700000000,
          },
        ],
      }),
      summary: "Move 1 messages to Trash",
      state: "pending",
      destinations: [],
      error: "",
      sessionAllow: false,
      draft: 0,
      draftEdit: false,
    };

    const { user, onDecide } = await setupPanel(approval);

    const select = screen.getByRole("combobox");
    await user.selectOptions(select, "archive_mail");

    const approveBtn = screen.getByRole("button", { name: /Archivieren \(1\)/ });
    expect(approveBtn).toBeInTheDocument();

    await user.click(approveBtn);

    expect(onDecide).toHaveBeenCalledTimes(1);
    const passedArgs = JSON.parse(onDecide.mock.calls[0][3] as string);
    expect(passedArgs.action).toBe("archive_mail");
    expect(passedArgs.ids).toEqual([1]);
  });

  test("allows inline editing of draft subject and body", async () => {
    const approval: Approval = {
      id: 44,
      run: 10,
      callId: "call_reply_1",
      tool: "reply_to_mail",
      args: JSON.stringify({
        id: 1,
        subject: "Re: Meeting notes",
        html: "<p>Original draft body</p>",
      }),
      summary: "Reply to Alice: Re: Meeting notes",
      state: "pending",
      destinations: [],
      error: "",
      sessionAllow: false,
      draft: 0,
      draftEdit: false,
    };

    const { user, onDecide } = await setupPanel(approval);

    // Click Edit button
    const editBtn = screen.getByRole("button", { name: /Bearbeiten/i });
    await user.click(editBtn);

    // Subject input and Body textarea appear
    const subjectInput = screen.getByDisplayValue("Re: Meeting notes");
    const bodyInput = screen.getByDisplayValue("Original draft body");

    await user.clear(subjectInput);
    await user.type(subjectInput, "Updated Subject");
    await user.clear(bodyInput);
    await user.type(bodyInput, "Edited reply message");

    // Click Allow
    const allowBtn = screen.getByRole("button", { name: /Erlauben|Allow|wit\.approval\.allow/i });
    await user.click(allowBtn);

    expect(onDecide).toHaveBeenCalledTimes(1);
    const passedArgs = JSON.parse(onDecide.mock.calls[0][3] as string);
    expect(passedArgs.subject).toBe("Updated Subject");
    expect(passedArgs.html).toBe("Edited reply message");
  });
});
