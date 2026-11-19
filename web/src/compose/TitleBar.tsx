/**
 * Compose/TitleBar: the window's top bar, which drags it, clear of the traffic lights: the
 * subject (or 新邮件), cut short when long, and the save status beside it.
 */
export function TitleBar({ title, status }: { title: string; status: string }) {
  return (
    <header className="compose-titlebar flex shrink-0 items-center gap-2 pr-4">
      <h1 className="min-w-0 truncate font-semibold text-sm">{title}</h1>
      <p className="shrink-0 text-muted-foreground text-xs">{status}</p>
    </header>
  );
}
