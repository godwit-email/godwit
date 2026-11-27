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
import { createPlatePlugin, PlateElement, type PlateElementProps } from "platejs/react";
import React, { createContext, useContext } from "react";
import { CID_SRC, deserialize, safeHref, toHTML } from "./html";

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

/** The draft's images as data: URLs by Content-ID (Go's ComposeState.Images), for DraftImagePlugin. */
export const DraftImages = createContext<Record<string, string>>({});

/** A draft image (`url` its cid:): shown from DraftImages; one not there yet shows as an empty box. */
function DraftImageElement(props: PlateElementProps) {
  const url = useContext(DraftImages)[String(props.element.url).slice("cid:".length)];
  return React.createElement(
    PlateElement,
    props,
    url
      ? React.createElement("img", {
          src: url,
          alt: "",
          contentEditable: false,
          className: "max-w-full select-none",
        })
      : React.createElement("span", {
          contentEditable: false,
          className: "block h-16 w-24 rounded-md bg-muted",
        }),
    props.children,
  );
}

/** An image in the body: an <img> whose src is CID_SRC, a void block. */
export const DraftImagePlugin = createPlatePlugin({
  key: "img",
  node: { isElement: true, isVoid: true, component: DraftImageElement },
  parsers: {
    html: {
      deserializer: {
        rules: [{ validNodeName: "IMG" }],
        parse: ({ element }) => {
          const url = element.getAttribute("src") ?? "";
          return CID_SRC.test(url) ? { type: "img", url } : undefined;
        },
      },
    },
  },
});

/**
 * The compose page formats: paragraph (core), bold, italic, underline, strikethrough,
 * font size, font family, link, bulleted and numbered list (list-classic ul/ol/li/lic),
 * block quote, the draft's images.
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
  DraftImagePlugin,
  SafePastePlugin,
  HeldPlugin,
];
