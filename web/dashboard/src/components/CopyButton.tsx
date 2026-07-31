import { useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";

// Used for endpoint URLs and the once-shown signing secret, where retyping by
// hand is both tedious and error-prone.
export default function CopyButton({ value, label = "Copy" }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false);

  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          // Clipboard access is denied on plain HTTP outside localhost, which
          // is a plausible self-hosted setup — say so instead of doing nothing.
          toast.error("Could not copy. Select the text and copy manually.");
        }
      }}
    >
      {copied ? "Copied" : label}
    </Button>
  );
}
