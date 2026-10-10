import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import type { SendWarning } from "@/mygo";
import type { T } from "@/shared/i18n";

function From({ w, t }: { w: SendWarning; t: T }) {
  return <AlertDescription>{t("compose.warn.from", w.plugin)}</AlertDescription>;
}

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
        <Alert variant="destructive">
          <AlertTitle>{blocked.message}</AlertTitle>
          <From w={blocked} t={t} />
        </Alert>
      )}
      {warnings.map((w) => (
        <Alert key={`${w.plugin}:${w.message}`}>
          <AlertTitle>{t("compose.warn", w.message)}</AlertTitle>
          <From w={w} t={t} />
        </Alert>
      ))}
      {warnings.length > 0 && !blocked && (
        <div>
          <Button variant="outline" disabled={frozen} onClick={() => !frozen && onSendAnyway()}>
            {t("compose.send_anyway")}
          </Button>
        </div>
      )}
      {conflict && (
        <Alert variant="destructive">
          <AlertTitle>{t("compose.conflict")}</AlertTitle>
          <div className="mt-1 flex gap-2">
            {latestFailed ? (
              <Button
                variant="outline"
                size="sm"
                disabled={frozen}
                onClick={() => !frozen && onRetry()}
              >
                {t("common.retry")}
              </Button>
            ) : (
              <>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={frozen}
                  onClick={() => !frozen && onKeepMine()}
                >
                  {t("compose.conflict.keep")}
                </Button>
                <Button
                  variant="outline"
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
