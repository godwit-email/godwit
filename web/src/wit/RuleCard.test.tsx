import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import type { Approval } from "@/mygo";
import { makeT } from "@/shared/i18n";
import { RuleCard, ruleSettled } from "./RuleCard";

afterEach(cleanup);

const t = makeT("zh-Hans");

const proposal = (actions: object[], destinations: string[]): Approval => ({
  id: 1,
  run: 10,
  callId: "c1",
  tool: "propose_rule",
  summary: "Create rule: Stripe",
  state: "pending",
  error: "",
  destinations,
  args: JSON.stringify({
    rule: {
      version: 1,
      name: "Stripe 发票转给会计",
      trigger: "receive",
      match: "all",
      conditions: [{ field: "from", op: "contains", value: "stripe.com" }],
      actions,
    },
    background: false,
    first_time: destinations,
    matches: 2,
    mail: [{ id: 5, from: "Stripe <s@stripe.com>", subject: "Invoice #1234", date: "" }],
    labels: {},
  }),
  sessionAllow: false,
  draft: 0,
  draftEdit: false,
});

test("a forward is saved only through the recipient confirmation", async () => {
  const decide = vi.fn();
  const a = proposal(
    [{ kind: "forward", to: "acct@company.example" }, { kind: "archive" }],
    ["acct@company.example"],
  );
  render(<RuleCard a={a} busy={false} decide={decide} onEdit={vi.fn()} t={t} />);
  expect(screen.getByText("收到新邮件 · 所有账户")).toBeTruthy();
  expect(screen.getByText("stripe.com").className).toContain("font-mono");
  expect(screen.getByText("外发")).toBeTruthy();
  expect(screen.getByText("现有邮件不会被转发，规则只处理保存之后收到的新邮件。")).toBeTruthy();
  await userEvent.click(screen.getByRole("button", { name: "保存规则" }));
  expect(decide).not.toHaveBeenCalled();
  // the footer turns into the confirmation; the rule stays in view
  expect(screen.getByRole("group", { name: "确认转发收件人" })).toBeTruthy();
  expect(screen.getByText("Stripe 发票转给会计")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "拒绝" })).toBeNull();
  expect(screen.getByText("· 以后修改收件人，需要再次确认")).toBeTruthy();
  await userEvent.click(screen.getByRole("button", { name: "返回" }));
  await userEvent.click(screen.getByRole("button", { name: "保存规则" }));
  await userEvent.click(screen.getByRole("button", { name: "确认收件人并保存" }));
  expect(decide).toHaveBeenCalledWith(
    true,
    JSON.stringify({ confirmed: ["acct@company.example"] }),
  );
});

test("reject and edit decide nothing else", async () => {
  const decide = vi.fn();
  const onEdit = vi.fn();
  render(
    <RuleCard
      a={proposal([{ kind: "archive" }], [])}
      busy={false}
      decide={decide}
      onEdit={onEdit}
      t={t}
    />,
  );
  await userEvent.click(screen.getByRole("button", { name: "在编辑器里修改" }));
  expect(onEdit).toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "拒绝" }));
  expect(decide).toHaveBeenCalledWith(false);
  await userEvent.click(screen.getByRole("button", { name: "保存规则" }));
  expect(decide).toHaveBeenLastCalledWith(true);
});

test("a rule for one account says so, and its matches open on a click", async () => {
  const a = proposal([{ kind: "archive" }], []);
  a.args = JSON.stringify({ ...JSON.parse(a.args), account_name: "Work" });
  render(<RuleCard a={a} busy={false} decide={vi.fn()} t={t} />);
  expect(screen.getByText("在 Work 收到新邮件")).toBeTruthy();
  expect(screen.queryByText("Invoice #1234")).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: "有 2 封符合" }));
  expect(screen.getByText("Invoice #1234")).toBeTruthy();
  expect(screen.getByText("Stripe")).toBeTruthy();
});

test("a file action shows its whole folder path", () => {
  const a = proposal(
    [{ kind: "pdf", dir: "/Users/me/Documents/Bills", naming: "{date} {subject}" }],
    [],
  );
  render(<RuleCard a={a} busy={false} decide={vi.fn()} t={t} />);
  expect(screen.getByText("导出 PDF 到「/Users/me/Documents/Bills」")).toBeTruthy();
});

test("a Wit action names the model's host, which needs no confirming", async () => {
  const decide = vi.fn();
  const a = proposal([{ kind: "draft_reply" }], ["api.openai.com"]);
  render(<RuleCard a={a} busy={false} decide={decide} t={t} />);
  expect(screen.getByText("外发")).toBeTruthy();
  expect(screen.getByText("api.openai.com")).toBeTruthy();
  await userEvent.click(screen.getByRole("button", { name: "保存规则" }));
  expect(decide).toHaveBeenCalledWith(true);
});

test("a proposal taken to the editor says so", () => {
  const a = {
    ...proposal([{ kind: "archive" }], []),
    state: "rejected",
    error: "the user opened the rule in the editor; it is saved only if they save it there",
  };
  expect(ruleSettled(a, t)).toBe("已在编辑器里打开，在那里保存后才会生效。");
  expect(ruleSettled({ ...a, error: "rejected by the user" }, t)).toBe("已拒绝，没有创建规则。");
});
