import Link from "next/link";
import { CURRICULUM, chapterTitle, type Locale } from "@/lib/curriculum";

export default function SessionNav({
  locale,
  activeSlug,
}: {
  locale: Locale;
  activeSlug?: string;
}) {
  return (
    <nav className="text-sm">
      <div className="text-xs uppercase tracking-wider text-[var(--fg-muted)] mb-2">
        {locale === "zh" ? "课程" : "Curriculum"}
      </div>
      <ul className="space-y-0.5">
        {CURRICULUM.map((c) => {
          const isActive = c.slug === activeSlug;
          const baseClasses =
            "flex items-baseline gap-2 px-2 py-1 rounded transition-colors";
          if (!c.available) {
            return (
              <li key={c.slug}>
                <span
                  className={`${baseClasses} cursor-not-allowed text-[var(--fg-muted)] opacity-60`}
                  title={locale === "zh" ? "尚未发布" : "Not yet published"}
                >
                  <span className="font-mono text-xs w-12 shrink-0">
                    {c.num}
                  </span>
                  <span className="leading-snug">
                    {chapterTitle(c, locale)}
                  </span>
                </span>
              </li>
            );
          }
          return (
            <li key={c.slug}>
              <Link
                href={`/${locale}/s/${c.slug}`}
                className={`${baseClasses} hover:bg-[var(--bg-elev)] ${
                  isActive
                    ? "bg-[var(--bg-elev)] text-[var(--accent)]"
                    : "text-[var(--fg)]"
                }`}
              >
                <span className="font-mono text-xs w-12 shrink-0">{c.num}</span>
                <span className="leading-snug">{chapterTitle(c, locale)}</span>
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
