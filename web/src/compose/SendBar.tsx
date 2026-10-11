import { useState } from "react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import type { T } from "@/shared/i18n";

/** The send button, discard (behind a confirm) and the note, at the bottom bar's end. */
export function SendBar({
  sending,
  frozen,
  onSend,
  onDiscard,
  t,
}: {
  sending: boolean;
  /** True while any operation runs: every control is inert. */
  frozen: boolean;
  onSend(): void;
  onDiscard(): void;
  t: T;
}) {
  const [asking, setAsking] = useState(false);
  const off = sending || frozen;
  return (
    <div className="ml-auto flex flex-wrap items-center gap-2">
      <Button className="px-4 font-semibold text-md" disabled={off} onClick={onSend}>
        {t("compose.send")}
      </Button>
      <Button
        variant="outline"
        className="px-3 font-normal text-md"
        disabled={off}
        onClick={() => setAsking(true)}
      >
        {t("compose.discard")}
      </Button>
      <p className="text-base text-muted-foreground">{t("compose.note")}</p>
      <AlertDialog open={asking} onOpenChange={setAsking}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("compose.discard.title")}</AlertDialogTitle>
            <AlertDialogDescription>{t("compose.discard.body")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              disabled={off}
              onClick={() => {
                if (off) return;
                setAsking(false);
                onDiscard();
              }}
            >
              {t("compose.discard.confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
