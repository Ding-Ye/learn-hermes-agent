import { loadUpstreamSnippet } from "@/lib/content";

// Server component. Loads ../upstream-readings/<file> at build time and
// renders it as a code block with a header that points back to the upstream
// path inferred from the snippet's leading "# Source:" comment.
export default async function UpstreamReader({
  file,
  locale,
}: {
  file: string;
  locale: "zh" | "en";
}) {
  const content = await loadUpstreamSnippet(file);
  if (!content) {
    return (
      <div className="my-4 p-3 rounded border border-dashed border-[var(--border)] text-[var(--fg-muted)] text-sm">
        {locale === "zh" ? "上游片段未找到：" : "Upstream snippet not found: "}
        <code>{file}</code>
      </div>
    );
  }
  const sourceMatch = content.match(/#\s*Source:\s*(.+)/);
  const source = sourceMatch ? sourceMatch[1].trim() : file;

  return (
    <figure className="my-6 rounded-lg border border-[var(--border)] overflow-hidden">
      <figcaption className="bg-[var(--bg-elev)] px-3 py-2 text-xs text-[var(--fg-muted)] border-b border-[var(--border)] flex items-center justify-between">
        <span>
          {locale === "zh" ? "上游源码 · " : "Upstream source · "}
          <code className="text-[var(--fg)]">{source}</code>
        </span>
        <span className="opacity-70">
          {locale === "zh" ? "教学摘录，含简化与注解" : "teaching excerpt, simplified + annotated"}
        </span>
      </figcaption>
      <pre className="text-xs leading-relaxed p-4 overflow-x-auto bg-[var(--code-bg)]">
        <code>{content}</code>
      </pre>
    </figure>
  );
}
