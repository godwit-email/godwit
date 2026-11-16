import { RiAlertLine } from "@remixicon/react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/dialog";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import type { SendWarning } from "@/mygo";
import type { T } from "@/shared/i18n";

function From({ w, t }: { w: SendWarning; t: T }) {
  return (
    <AlertDescription className="text-sm">{t("compose.warn.from", w.plugin)}</AlertDescription>
  );
}

// The design's notice: a filled box, no outline; a warning in the theme's warning colours.
const box = "gap-1 border-0 px-3.5 py-3 text-sm";
const warn = `${box} bg-(--warning-bg) text-(--warning-text) **:data-[slot=alert-description]:text-(--warning-text)`;
const danger = `${box} bg-destructive/10`;

/** Plugin warnings (Send then reads 仍然发送), a blocking refusal, and the save conflict as a dialog. */
export function Notices({
  warnings,
  blocked,
  conflict,
  latestFailed,
  frozen,
  onKeepMine,
  onUseLatest,
  onRetry,
  t,
}: {
  warnings: SendWarning[];
  blocked: SendWarning | null;
  conflict: boolean;
  latestFailed: boolean;
  frozen: boolean;
  onKeepMine(): void;
  onUseLatest(): void;
  onRetry(): void;
  t: T;
}) {
  return (
    <div className="flex flex-col gap-2">
      {blocked && (
        <Alert variant="destructive" className={danger}>
          <AlertTitle>{blocked.message}</AlertTitle>
          <From w={blocked} t={t} />
        </Alert>
      )}
      {warnings.length > 0 && (
        // what the plugins found, one notice; Send itself reads 仍然发送 (SendBar's anyway)
        <Alert className={warn}>
          <RiAlertLine />
          {warnings.map((w) => (
            <div key={`${w.plugin}:${w.message}`} className="flex flex-col gap-0.5">
              <AlertTitle>{t("compose.warn", w.message)}</AlertTitle>
              <From w={w} t={t} />
            </div>
          ))}
        </Alert>
      )}
      {/* a choice to make before anything else saves: a dialog that waits for it */}
      <Dialog open={conflict}>
        <DialogContent alert>
          <DialogHeader>
            <DialogTitle>{t("compose.conflict")}</DialogTitle>
            <DialogDescription>{t("compose.conflict.desc")}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            {latestFailed ? (
              <Button disabled={frozen} onClick={() => !frozen && onRetry()}>
                {t("common.retry")}
              </Button>
            ) : (
              <>
                <Button variant="outline" disabled={frozen} onClick={() => !frozen && onKeepMine()}>
                  {t("compose.conflict.keep")}
                </Button>
                <Button disabled={frozen} onClick={() => !frozen && onUseLatest()}>
                  {t("compose.conflict.latest")}
                </Button>
              </>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
