import { useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";

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
          toast.error("Could not copy. Select the text and copy manually.");
        }
      }}
    >
      {copied ? "Copied" : label}
    </Button>
  );
}
