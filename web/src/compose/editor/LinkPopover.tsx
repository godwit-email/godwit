import { type ReactElement, useEffect, useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import type { T } from "./Toolbar";

/** The link URL popover, opened from `trigger`; onApply returns false for a URL it refuses. */
export function LinkPopover(props: {
  open: boolean;
  onOpenChange(open: boolean): void;
  trigger: ReactElement;
  onApply(url: string): boolean;
  t: T;
}) {
  const { open, onOpenChange, trigger, onApply, t } = props;
  const id = useId();
  const [url, setUrl] = useState("");
  const [bad, setBad] = useState(false);
  useEffect(() => {
    if (open) {
      setUrl("");
      setBad(false);
    }
  }, [open]);
  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger render={trigger} />
      <PopoverContent>
        <form
          className="flex flex-col gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (onApply(url)) onOpenChange(false);
            else setBad(true);
          }}
        >
          <Label htmlFor={id}>{t("compose.link.url")}</Label>
          <div className="flex gap-2">
            <Input
              id={id}
              placeholder="https://"
              value={url}
              aria-invalid={bad}
              onChange={(e) => {
                setUrl(e.target.value);
                setBad(false);
              }}
            />
            <Button type="submit">{t("compose.link.apply")}</Button>
          </div>
        </form>
      </PopoverContent>
    </Popover>
  );
}
