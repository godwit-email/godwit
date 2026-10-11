import { Button } from "@/components/ui/button";
import type { T } from "@/shared/i18n";

/** The send button, at the bottom bar's end (ml-auto keeps it right when the bar wraps). */
export function SendBar({
  sending,
  frozen,
  onSend,
  t,
}: {
  sending: boolean;
  /** True while any operation runs: the button is inert. */
  frozen: boolean;
  onSend(): void;
  t: T;
}) {
  return (
    <Button
      className="ml-auto border-0 px-4 font-semibold text-md"
      disabled={sending || frozen}
      onClick={onSend}
    >
      {t("compose.send")}
    </Button>
  );
}
