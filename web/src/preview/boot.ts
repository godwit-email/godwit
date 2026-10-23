// Dev-only entry of preview.html: installs the fake window.mygo for ?page=&state=&theme=, then
// imports that page's own entry (which reads window.mygo as it loads).
import type { BodyChange, ReaderBody, WitEvent } from "@/mygo";
import { installFake } from "./fake";
import * as fx from "./fixtures";

const q = new URLSearchParams(location.search);
const page = q.get("page") ?? "compose";
const state = q.get("state") ?? "";
const mode = q.get("theme") === "dark" ? "dark" : "light";
const nothing = async () => {};
const runStart = { run: 2, conversation: 1, turn: "2", revision: 1, conflict: false };

const pages: Record<string, { states: string[]; entry: () => Promise<unknown>; setup(): void }> = {
  compose: {
    states: [
      "plain",
      "streaming",
      "inline",
      "rewrite",
      "applied",
      "cmdk",
      "signature",
      "translate",
    ],
    entry: () => import("@/compose/main"),
    setup() {
      const withBody = ["inline", "rewrite", "cmdk"].includes(state);
      let revision = 1;
      let followed = false;
      const body = () => (revision > 1 || withBody ? fx.compose(mode, true).body : "");
      const changed = (): BodyChange => ({
        revision,
        attachments: [],
        conflict: false,
        signatureHtml: "",
        updatedAt: Date.now(),
        body: body(),
        subject: fx.compose(mode, true).subject,
      });
      const history = () =>
        state === "streaming"
          ? fx.streaming()
          : state === "applied"
            ? fx.applied()
            : state === "inline" || state === "rewrite"
              ? fx.pending(state === "inline")
              : state === "cmdk"
                ? fx.applied()
                : fx.noWit();
      const emit = installFake({
        "ComposePage.Load": async () => ({
          ...fx.compose(mode, withBody),
          revision,
          ...(state === "translate" && {
            wit: { ...fx.compose(mode, withBody).wit, translateOnly: true },
          }),
          ...(state === "signature" && {
            signatureHtml: `<p>Alex Chen<br>Partnerships · Example Co.</p><img src="${fx.logo}">`, // no "-- ": SignatureDelimiter is off by default
          }),
        }),
        "ComposePage.Latest": async () => ({ ...fx.compose(mode, true), revision }),
        "ComposePage.WitInfo": async () => ({
          ...fx.compose(mode, false).wit,
          translateOnly: state === "translate",
        }),
        "ComposePage.Follow": async () => {
          if (!followed) {
            followed = true;
            const evs: [number, WitEvent][] =
              state === "streaming"
                ? fx.streamEvents.map((e, i) => [300 + i * 200, e])
                : state === "applied"
                  ? [[800, fx.doneEvent]]
                  : [];
            for (const [ms, e] of evs)
              setTimeout(() => {
                if (e.kind === "done") revision = 2;
                emit("wit", e);
              }, ms);
          }
          return history();
        },
        "ComposePage.Review": async () => fx.review(state === "inline"),
        "ComposePage.Save": async (e: { revision: number }) => ({
          revision: e.revision,
          conflict: false,
        }),
        "ComposePage.Closed": nothing,
        "ComposePage.Stop": nothing,
        "ComposePage.OpenLink": nothing,
        "ComposePage.OpenModels": nothing,
        "ComposePage.Models": async () => fx.witModels,
        "ComposePage.OpenCited": nothing,
        "ComposePage.CheckCited": nothing,
        "ComposePage.Ask": async () => runStart,
        "ComposePage.Skill": async () => runStart,
        "ComposePage.Rewrite": async () => runStart,
        "ComposePage.Decide": async () => changed(),
        "ComposePage.DecideAll": async () => changed(),
        "ComposePage.Finish": async () => changed(),
        "ComposePage.Undo": async () => changed(),
        "ComposePage.Send": async () => ({
          sent: false,
          conflict: false,
          warnings: [],
          digest: "",
          blocked: null,
        }),
      });
      if (state === "cmdk") setTimeout(selectAndOpenCmdK, 1500);
    },
  },
  wit: {
    // plus: the + menu open; picker: 引用邮件's search with hits; quoted: a quoted mail's chip
    states: ["conversation", "empty", "plus", "picker", "quoted"],
    entry: () => import("@/wit-page/main"),
    setup() {
      const conv = state === "empty" ? 0 : 1;
      // the page is clear over the native window's material (wit.html's body): show its colour
      document.body.style.background = "var(--window)";
      installFake({
        "WitPage.Load": async () => fx.witPage(mode, conv),
        "WitPage.Follow": async () => (conv ? fx.witConversation() : fx.witEmpty()),
        "WitPage.Navigate": nothing,
        "WitPage.OpenCited": nothing,
        "WitPage.OpenLink": nothing,
        "WitPage.OpenModels": nothing,
        "WitPage.Models": async () => fx.witModels,
        "WitPage.Ask": async () => runStart,
        "WitPage.Stop": nothing,
        "WitPage.Decide": nothing,
        "WitPage.DecideAll": nothing,
        "WitPage.DecideSession": nothing,
        "WitPage.SearchMail": async () => fx.mailHits,
      });
      if (["plus", "picker", "quoted"].includes(state)) setTimeout(() => quote(state), 1200);
    },
  },
  signature: {
    states: ["filled", "image", "layout"],
    entry: () => import("@/signature/main"),
    setup() {
      const emit = installFake({
        "SignaturePage.State": async () => fx.signature(mode),
        "SignaturePage.Load": async () =>
          ({ image: fx.signatureHtmlImage, layout: fx.signatureLayout })[state] ?? fx.signatureHtml,
        "SignaturePage.PickImage": async () => {
          setTimeout(() => emit("signatureImage", { url: fx.logo, error: "" }));
        },
        "SignaturePage.AskLink": async () => {
          setTimeout(() => emit("signatureLink", { url: "https://example.com/", ok: true }));
        },
        "SignaturePage.Save": nothing,
        "SignaturePage.Saved": nothing,
        "SignaturePage.EditHTML": nothing,
      });
    },
  },
  reader: {
    states: ["basic"],
    entry: () => import("@/reader/main"),
    setup() {
      const body: ReaderBody = {
        message: 41,
        version: 1,
        html: "<p>Following up on our call last week, I put together a draft proposal for the Q4 partnership.</p><p>Best,<br>Lena</p>",
        plain: "",
        remote: false,
        blocked: 0,
        cut: false,
        theme: fx.theme(mode),
        locale: "zh-Hans",
      };
      installFake({
        "ReaderPage.Load": async () => body,
        "ReaderPage.Height": nothing,
        "ReaderPage.Navigate": nothing,
        "ReaderPage.OpenLink": nothing,
        "ReaderPage.Search": nothing,
        "ReaderPage.Wheel": nothing,
      });
    },
  },
};

/** Opens the + menu, then (picker, quoted) 引用邮件 with a search, then (quoted) picks a hit. */
async function quote(step: string) {
  const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));
  document.querySelector<HTMLElement>("button[aria-label='添加']")?.click();
  await wait(300);
  if (step === "plus") return;
  [...document.querySelectorAll<HTMLElement>("[role=menuitem]")]
    .find((e) => e.textContent?.includes("引用邮件"))
    ?.click();
  await wait(300);
  const input = document.querySelector<HTMLInputElement>("input[role=combobox]");
  if (!input) return;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(input, "Q4");
  input.dispatchEvent(new Event("input", { bubbles: true }));
  await wait(500);
  if (step === "picker") return;
  document.querySelector<HTMLElement>("[role=option]")?.click();
}

/** ⌘K on the second paragraph, as a user selecting it and pressing the shortcut does; retried until the page takes it. */
function selectAndOpenCmdK(tries = 0) {
  const again = () => tries < 40 && setTimeout(() => selectAndOpenCmdK(tries + 1), 250);
  const ed = document.querySelector<HTMLElement>('[contenteditable="true"]');
  const p = ed?.querySelectorAll("[data-slate-node=element]")[1];
  if (!ed || !p) return again();
  ed.focus();
  const r = document.createRange();
  r.selectNodeContents(p);
  const s = getSelection();
  s?.removeAllRanges();
  s?.addRange(r);
  document.dispatchEvent(new Event("selectionchange"));
  setTimeout(() => {
    ed.dispatchEvent(
      new KeyboardEvent("keydown", { key: "k", metaKey: true, bubbles: true, cancelable: true }),
    );
    // the mockup's instruction, typed the way React hears it
    setTimeout(() => {
      const input = document.querySelector<HTMLInputElement>("input[aria-label='修改选中的内容']");
      if (!input) return again();
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(
        input,
        "语气委婉一点，再提一句期待合作",
      );
      input.dispatchEvent(new Event("input", { bubbles: true }));
    }, 200);
  }, 200);
}

const p = pages[page];
if (p?.states.includes(state)) {
  p.setup();
  void p.entry();
} else {
  const opts = Object.entries(pages).flatMap(([n, v]) =>
    v.states.map((s) => `?page=${n}&state=${s}`),
  );
  document.body.textContent = `preview: unknown page/state. Try: ${opts.join("  ")}`;
}
