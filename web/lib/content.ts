import fs from "node:fs/promises";
import path from "node:path";
import matter from "gray-matter";
import { unified } from "unified";
import remarkParse from "remark-parse";
import remarkGfm from "remark-gfm";
import remarkRehype from "remark-rehype";
import rehypeRaw from "rehype-raw";
import rehypeSlug from "rehype-slug";
import rehypeAutolinkHeadings from "rehype-autolink-headings";
import rehypePrettyCode from "rehype-pretty-code";
import rehypeStringify from "rehype-stringify";
import type { Locale } from "./curriculum";

const REPO_ROOT = path.resolve(process.cwd(), "..");
const DOCS_ROOT = path.join(REPO_ROOT, "docs");

export type DocFrontmatter = {
  title: string;
  chapter: number | string;
  slug: string;
  est_read_min?: number;
};

export type DocPayload = {
  frontmatter: DocFrontmatter;
  html: string;
};

const processor = unified()
  .use(remarkParse)
  .use(remarkGfm)
  .use(remarkRehype, { allowDangerousHtml: true })
  .use(rehypeRaw)
  .use(rehypeSlug)
  .use(rehypeAutolinkHeadings, { behavior: "wrap" })
  .use(rehypePrettyCode, {
    theme: "github-dark-dimmed",
    keepBackground: false,
  })
  .use(rehypeStringify, { allowDangerousHtml: true });

export async function loadDoc(
  locale: Locale,
  slug: string,
): Promise<DocPayload | null> {
  const file = path.join(DOCS_ROOT, locale, `${slug}.md`);
  let raw: string;
  try {
    raw = await fs.readFile(file, "utf8");
  } catch {
    return null;
  }
  const parsed = matter(raw);
  const html = String(await processor.process(parsed.content));
  return {
    frontmatter: parsed.data as DocFrontmatter,
    html,
  };
}

export async function loadUpstreamSnippet(name: string): Promise<string | null> {
  const file = path.join(REPO_ROOT, "upstream-readings", name);
  try {
    return await fs.readFile(file, "utf8");
  } catch {
    return null;
  }
}
