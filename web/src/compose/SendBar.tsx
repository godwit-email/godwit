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

/** The send button, discard (behind a confirm), the note and the save status. */
export function SendBar({
  sending,
  status,
  savedAt,
  onSend,
  onDiscard,
  t,
}: {
  sending: boolean;
  status: "saving" | "saved" | "unsaved";
  savedAt: string;
  onSend(): void;
  onDiscard(): void;
  t: T;
}) {
  const [asking, setAsking] = useState(false);
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2">
        <Button disabled={sending} onClick={onSend}>
          {t("compose.send")}
        </Button>
        <Button variant="ghost" disabled={sending} onClick={() => setAsking(true)}>
          {t("compose.discard")}
        </Button>
        <span className="ml-auto text-muted-foreground text-xs">
          {status === "saved" ? t("compose.status.saved", savedAt) : t(`compose.status.${status}`)}
        </span>
      </div>
      <p className="text-muted-foreground text-xs">{t("compose.note")}</p>
      <AlertDialog open={asking} onOpenChange={setAsking}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("compose.discard.title")}</AlertDialogTitle>
            <AlertDialogDescription>{t("compose.discard.body")}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={onDiscard}>
              {t("compose.discard.confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
