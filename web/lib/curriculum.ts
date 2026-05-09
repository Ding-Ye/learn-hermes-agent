// The locked curriculum from the plan. SessionNav and the landing page both
// read from this single source of truth. Slugs match docs/{zh,en}/<slug>.md.
//
// "available: false" means the chapter exists in the curriculum but its
// docs aren't written yet — the link will render but go to a placeholder.

export type ChapterMeta = {
  slug: string;
  num: string; // "s01", "s02", "s_full"
  title: { zh: string; en: string };
  available: boolean;
};

export const CURRICULUM: ChapterMeta[] = [
  {
    slug: "s01-minimum-loop",
    num: "s01",
    title: { zh: "最小 agent loop", en: "Minimum agent loop" },
    available: true,
  },
  {
    slug: "s02-tool-registry",
    num: "s02",
    title: { zh: "Tool 注册系统", en: "Tool registry" },
    available: true,
  },
  {
    slug: "s03-skills",
    num: "s03",
    title: {
      zh: "Skills 系统：Markdown prompt + 模板 + shell 展开",
      en: "Skills: Markdown prompts + templates + shell expansion",
    },
    available: true,
  },
  {
    slug: "s04-session",
    num: "s04",
    title: { zh: "Session 持久化", en: "Session persistence" },
    available: true,
  },
  {
    slug: "s05-memory",
    num: "s05",
    title: {
      zh: "Memory Provider + FTS5 实现",
      en: "Memory Provider + FTS5 implementation",
    },
    available: true,
  },
  {
    slug: "s06-plugins-curator",
    num: "s06",
    title: {
      zh: "Plugin 系统 + Curator 自改进循环",
      en: "Plugin system + Curator self-improvement loop",
    },
    available: true,
  },
  {
    slug: "s07-mcp",
    num: "s07",
    title: { zh: "MCP 集成", en: "MCP integration" },
    available: true,
  },
  {
    slug: "s08-terminal-backends",
    num: "s08",
    title: {
      zh: "Terminal Backend 工厂",
      en: "Terminal backend factory",
    },
    available: true,
  },
  {
    slug: "s09-multiprocess",
    num: "s09",
    title: {
      zh: "Multi-process 架构：CLI/Gateway/Scheduler",
      en: "Multi-process architecture: CLI/Gateway/Scheduler",
    },
    available: true,
  },
  {
    slug: "s10-platforms",
    num: "s10",
    title: {
      zh: "Gateway 平台适配器（Telegram + Discord）",
      en: "Gateway platform adapters (Telegram + Discord)",
    },
    available: true,
  },
  {
    slug: "s_full-integration",
    num: "s_full",
    title: { zh: "端到端集成", en: "End-to-end integration" },
    available: true,
  },
  {
    slug: "appendix-a-atropos-rl",
    num: "A",
    title: {
      zh: "附录 A · Atropos / RL 心智模型",
      en: "Appendix A · Atropos / RL mental model",
    },
    available: true,
  },
  {
    slug: "appendix-b-upstream-map",
    num: "B",
    title: {
      zh: "附录 B · 上游源码导读地图",
      en: "Appendix B · Upstream source-reading map",
    },
    available: true,
  },
];

export type Locale = "zh" | "en";

export function chapterTitle(c: ChapterMeta, locale: Locale): string {
  return c.title[locale];
}
