"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import type { Locale } from "@/lib/curriculum";

export default function LangSwitch({ locale }: { locale: Locale }) {
  const pathname = usePathname() ?? "/";
  const other: Locale = locale === "zh" ? "en" : "zh";
  const swapped = pathname.replace(/^\/(zh|en)(?=\/|$)/, `/${other}`);
  const label = locale === "zh" ? "EN" : "中";
  return (
    <Link
      href={swapped}
      className="text-sm px-2 py-1 rounded border border-[var(--border)] text-[var(--fg-muted)] hover:text-[var(--fg)] hover:border-[var(--accent-soft)]"
    >
      {label}
    </Link>
  );
}
