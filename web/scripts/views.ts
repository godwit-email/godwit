// Builds godwit mcp's show tool views (ADR 0022): src/mcp/<view>.view.ts and view.css inlined
// into one self-contained ../mcpserver/views/<view>.html each, which Go embeds. web:check reruns
// this and fails on any diff, so the embedded copy is always the built one.
export {};

const views = ["draft", "mail"];

const dir = `${import.meta.dir}/..`;
const css = await Bun.file(`${dir}/src/mcp/view.css`).text();
for (const view of views) {
  const out = await Bun.build({
    entrypoints: [`${dir}/src/mcp/${view}.view.ts`],
    target: "browser",
    minify: true,
  });
  if (!out.success) throw new AggregateError(out.logs, `views: ${view}`);
  const js = (await out.outputs[0].text()).replaceAll("</script", "<\\/script");
  // The host's CSP declares no domains; this one says the same inside the file.
  const html = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'">
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>${css}</style>
</head>
<body>
<script type="module">${js}</script>
</body>
</html>
`;
  await Bun.write(`${dir}/../mcpserver/views/${view}.html`, html);
}
