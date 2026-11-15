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

/** Plugin warnings (sendable anyway), a blocking refusal and the save conflict. */
export function Notices({
  warnings,
  blocked,
  conflict,
  latestFailed,
  frozen,
  onSendAnyway,
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
  onSendAnyway(): void;
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
        // every warning in one notice, 仍然发送 inside it, as the conflict's buttons are in its
        <Alert className={warn}>
          {warnings.map((w) => (
            <div key={`${w.plugin}:${w.message}`} className="flex flex-col gap-1">
              <AlertTitle>{t("compose.warn", w.message)}</AlertTitle>
              <From w={w} t={t} />
            </div>
          ))}
          {!blocked && (
            <div className="mt-2 flex gap-1.5">
              <Button
                variant="warning"
                size="sm"
                disabled={frozen}
                onClick={() => !frozen && onSendAnyway()}
              >
                {t("compose.send_anyway")}
              </Button>
            </div>
          )}
        </Alert>
      )}
      {conflict && (
        <Alert variant="destructive" className={danger}>
          <AlertTitle>{t("compose.conflict")}</AlertTitle>
          <div className="mt-2 flex gap-1.5">
            {latestFailed ? (
              <Button
                variant="danger"
                size="sm"
                disabled={frozen}
                onClick={() => !frozen && onRetry()}
              >
                {t("common.retry")}
              </Button>
            ) : (
              <>
                <Button
                  variant="danger"
                  size="sm"
                  disabled={frozen}
                  onClick={() => !frozen && onKeepMine()}
                >
                  {t("compose.conflict.keep")}
                </Button>
                <Button
                  variant="danger"
                  size="sm"
                  disabled={frozen}
                  onClick={() => !frozen && onUseLatest()}
                >
                  {t("compose.conflict.latest")}
                </Button>
              </>
            )}
          </div>
        </Alert>
      )}
    </div>
  );
}
