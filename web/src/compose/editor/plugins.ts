import { BlockquotePlugin, BoldPlugin, ItalicPlugin } from "@platejs/basic-nodes/react";
import { LinkPlugin } from "@platejs/link/react";
import { ListPlugin, TaskListPlugin } from "@platejs/list-classic/react";
import { type AnyPluginConfig, createSlatePlugin, NodeApi } from "platejs";
import { deserialize, safeHref, toHTML } from "./html";

/**
 * Owns paste and drop: nothing reaches Plate's parsers or Slate's fragment insert. A Slate
 * fragment (copied from an editor) is written through toHTML, which keeps only the format set;
 * HTML takes fromHTML's path (inert parse, loaders stripped); when that yields nothing, the plain
 * text goes in as paragraphs. Files are not taken.
 */
const SafePastePlugin = createSlatePlugin({ key: "safePaste" }).overrideEditor(({ editor }) => ({
  transforms: {
    insertData(data: DataTransfer) {
      let html = data.getData("text/html");
      const fragment = data.getData("application/x-slate-fragment");
      if (fragment) {
        try {
          html = toHTML(JSON.parse(decodeURIComponent(atob(fragment))));
        } catch {}
      }
      const nodes = html ? deserialize(editor, html) : [];
      if (nodes.some((n) => NodeApi.string(n) !== "")) {
        editor.tf.insertFragment(nodes);
        return;
      }
      const text = data.getData("text/plain");
      if (!text) return;
      text.split(/\r\n|\r|\n/).forEach((line, i) => {
        if (i) editor.tf.insertBreak();
        if (line) editor.tf.insertText(line);
      });
    },
  },
}));

/**
 * The native compose page's formats and nothing else: paragraph (core), bold, italic, link,
 * bulleted and numbered list (list-classic ul/ol/li/lic; its task list disabled), block quote.
 */
export const plugins: AnyPluginConfig[] = [
  BoldPlugin,
  ItalicPlugin,
  LinkPlugin.configure({
    render: { as: "a" },
    options: { allowedSchemes: ["http", "https", "mailto"], isUrl: (u: string) => !!safeHref(u) },
  }),
  ListPlugin.configurePlugin(TaskListPlugin, { enabled: false }),
  BlockquotePlugin,
  SafePastePlugin,
];
