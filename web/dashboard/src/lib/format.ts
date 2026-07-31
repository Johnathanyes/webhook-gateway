const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

// Timestamps come back as RFC 3339 strings; render them in the viewer's own
// locale and timezone rather than UTC.
export function formatDateTime(value: string | null | undefined, fallback = "—") {
  if (!value) return fallback;
  return dateTime.format(new Date(value));
}
