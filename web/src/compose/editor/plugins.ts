import {
  BlockquotePlugin,
  BoldPlugin,
  ItalicPlugin,
  StrikethroughPlugin,
  UnderlinePlugin,
} from "@platejs/basic-nodes/react";
import { LinkPlugin } from "@platejs/link/react";
import { ListPlugin, TaskListPlugin } from "@platejs/list-classic/react";
import { type AnyPluginConfig, createSlatePlugin, NodeApi } from "platejs";
import { createPlatePlugin } from "platejs/react";
import React from "react";
import { deserialize, safeHref, toHTML } from "./html";

/**
 * Owns paste and drop: nothing reaches Plate's parsers or Slate's fragment insert. A Slate
 * fragment (copied from an editor) is written through toHTML, which keeps only the format set;
 * HTML takes fromHTML's path (inert parse, loaders stripped); when that yields nothing, the plain
 * text goes in as paragraphs. Files are not taken.
 */
export const SafePastePlugin = createSlatePlugin({ key: "safePaste" }).overrideEditor(
  ({ editor }) => ({
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
        // The caret's marks (the leaf's, or what the user toggled, {} included) for every line:
        // insertBreak clears them, and the new line would take the split leaf's instead.
        const marks = editor.api.marks() ?? {};
        text.split(/\r\n|\r|\n/).forEach((line, i) => {
          if (i) editor.tf.insertBreak();
          if (line) {
            editor.marks = { ...marks };
            editor.tf.insertText(line);
          }
        });
      },
    },
  }),
);

/** Links of the forms safeHref accepts only. */
export const linkPlugin = LinkPlugin.configure({
  render: { as: "a" },
  options: { allowedSchemes: ["http", "https", "mailto"], isUrl: (u: string) => !!safeHref(u) },
});

/** ⌘K's selection marked while its popover has focus (Editor's `held` decoration); never saved. */
export const HeldPlugin = createPlatePlugin({
  key: "held",
  node: { isLeaf: true, props: { className: "bg-border" } },
});

export const FontSizePlugin = createPlatePlugin({
  key: "fontSize",
  node: { isLeaf: true },
  render: {
    node: ({ children, leaf }) =>
      React.createElement(
        "span",
        { style: { fontSize: (leaf as Record<string, unknown>).fontSize as string } },
        children,
      ),
  },
  parsers: {
    html: {
      deserializer: {
        rules: [{ validStyle: { fontSize: "*" } }],
        parse: ({ element }) => ({ fontSize: element.style.fontSize }),
      },
    },
  },
});

export const FontFamilyPlugin = createPlatePlugin({
  key: "fontFamily",
  node: { isLeaf: true },
  render: {
    node: ({ children, leaf }) =>
      React.createElement(
        "span",
        { style: { fontFamily: (leaf as Record<string, unknown>).fontFamily as string } },
        children,
      ),
  },
  parsers: {
    html: {
      deserializer: {
        rules: [{ validStyle: { fontFamily: "*" } }],
        parse: ({ element }) => ({ fontFamily: element.style.fontFamily }),
      },
    },
  },
});

/**
 * The compose page formats: paragraph (core), bold, italic, underline, strikethrough,
 * font size, font family, link, bulleted and numbered list (list-classic ul/ol/li/lic),
 * block quote.
 */
export const plugins: AnyPluginConfig[] = [
  BoldPlugin,
  ItalicPlugin,
  UnderlinePlugin,
  StrikethroughPlugin,
  FontSizePlugin,
  FontFamilyPlugin,
  linkPlugin,
  ListPlugin.configurePlugin(TaskListPlugin, { enabled: false }),
  BlockquotePlugin,
  SafePastePlugin,
  HeldPlugin,
];
