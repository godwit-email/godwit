import { BlockquotePlugin, BoldPlugin, ItalicPlugin } from "@platejs/basic-nodes/react";
import { LinkPlugin } from "@platejs/link/react";
import { ListPlugin } from "@platejs/list-classic/react";
import { type AnyPluginConfig, createSlatePlugin } from "platejs";
import { deserialize, safeHref } from "./html";

/** Pasted HTML takes fromHTML's path (inert parse, loaders stripped); it runs before Plate's own HTML parser. */
const SafePastePlugin = createSlatePlugin({
  key: "safePaste",
  parser: { format: "text/html", deserialize: ({ editor, data }) => deserialize(editor, data) },
});

/**
 * The native compose page's formats and nothing else: paragraph (core), bold, italic, link,
 * bulleted and numbered list (list-classic ul/ol/li/lic), block quote.
 * list-classic also registers its task list type, which has no HTML parser and no control here.
 */
export const plugins: AnyPluginConfig[] = [
  BoldPlugin,
  ItalicPlugin,
  LinkPlugin.configure({
    render: { as: "a" },
    options: { allowedSchemes: ["http", "https", "mailto"], isUrl: (u: string) => !!safeHref(u) },
  }),
  ListPlugin,
  BlockquotePlugin,
  SafePastePlugin,
];
