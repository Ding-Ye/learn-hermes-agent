import { notFound } from "next/navigation";
import Link from "next/link";
import { loadDoc } from "@/lib/content";
import UpstreamReader from "@/components/UpstreamReader";
import { CURRICULUM, type Locale } from "@/lib/curriculum";

export async function generateStaticParams() {
  const params: { locale: string; slug: string }[] = [];
  for (const c of CURRICULUM) {
    if (!c.available) continue;
    for (const locale of ["zh", "en"] as const) {
      params.push({ locale, slug: c.slug });
    }
  }
  return params;
}

export default async function DocPage({
  params,
}: {
  params: Promise<{ locale: string; slug: string }>;
}) {
  const { locale, slug } = await params;
  if (locale !== "zh" && locale !== "en") notFound();
  const l = locale as Locale;

  const doc = await loadDoc(l, slug);
  if (!doc) notFound();

  // Determine the upstream snippet name conventionally (slug → sNN-name → first part).
  // s01-minimum-loop → s01-loop.py.  Adjust here if the convention differs per chapter.
  const upstreamFile = guessUpstreamFile(slug);

  const idx = CURRICULUM.findIndex((c) => c.slug === slug);
  const prev = idx > 0 ? CURRICULUM[idx - 1] : null;
  const next = idx >= 0 && idx < CURRICULUM.length - 1 ? CURRICULUM[idx + 1] : null;

  return (
    <article className="prose-doc">
      <div
        // Doc body (already includes its own h1 and the in-doc "Upstream
        // Source Reading" section as a code block — for s01 that fenced
        // block has language "upstream:..." which currently renders as a
        // plain code block. The dedicated server component below also
        // surfaces the file in a richer panel.)
        dangerouslySetInnerHTML={{ __html: doc.html }}
      />
      {upstreamFile && (
        <section className="mt-10 pt-6 border-t border-[var(--border)]">
          <h2 className="!mt-0">
            {l === "zh" ? "上游源码 · 完整摘录" : "Upstream source · full excerpt"}
          </h2>
          <UpstreamReader file={upstreamFile} locale={l} />
        </section>
      )}
      <nav className="mt-10 flex justify-between text-sm border-t border-[var(--border)] pt-5">
        <span>
          {prev?.available ? (
            <Link href={`/${l}/s/${prev.slug}`}>← {prev.num}</Link>
          ) : (
            <span className="text-[var(--fg-muted)]">—</span>
          )}
        </span>
        <span>
          {next?.available ? (
            <Link href={`/${l}/s/${next.slug}`}>{next.num} →</Link>
          ) : (
            <span className="text-[var(--fg-muted)]">
              {l === "zh" ? "下一节尚未发布" : "next chapter not yet"}
            </span>
          )}
        </span>
      </nav>
    </article>
  );
}

function guessUpstreamFile(slug: string): string | null {
  // Map docs/<locale>/<slug>.md → upstream-readings/<file>.py
  // Convention: take "sNN" prefix + first descriptor word.
  // s01-minimum-loop → s01-loop.py (we keep the existing file name).
  const map: Record<string, string> = {
    "s01-minimum-loop": "s01-loop.py",
    "s02-tool-registry": "s02-tool-registry.py",
    "s03-skills": "s03-skills.py",
    "s04-session": "s04-session.py",
    "s05-memory": "s05-memory.py",
  };
  return map[slug] ?? null;
}
