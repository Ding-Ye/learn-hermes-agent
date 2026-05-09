# Upstream source reading for s03 · Skills
#
# Sources:
#   - NousResearch/hermes-agent · tools/skills_tool.py     (~800 lines)
#   - NousResearch/hermes-agent · agent/skill_preprocessing.py
#   - NousResearch/hermes-agent · hermes_cli/curator.py    (lifecycle)
# License: see https://github.com/NousResearch/hermes-agent/blob/main/LICENSE
#
# Two-file teaching excerpt: how skills are DISCOVERED and how they are
# EXPANDED. Plus a glance at curator (the truth behind the
# "self-improving" tagline). Annotations inline; line numbers are local.


# ============================================================================
# tools/skills_tool.py  --  discovery + registry registration
# ============================================================================

from pathlib import Path
from typing import Iterable, Optional

# Hermes scans multiple directories. Default is the user-private location;
# config.yaml can add organisation- or team-shared directories.
DEFAULT_SKILL_DIRS = [
    Path.home() / ".hermes" / "skills",
]

EXCLUDED_DIRS = {".git", ".hub", ".archive"}


def iter_skill_index_files(scan_dir: Path, index_name: str = "SKILL.md") -> Iterable[Path]:
    """Recursively yield every <subdir>/SKILL.md under scan_dir, skipping
    EXCLUDED_DIRS along the way. Category is inferred from path depth:

        skills/mlops/axolotl/SKILL.md  ->  category 'mlops'
    """
    for path in scan_dir.rglob(index_name):
        if any(part in EXCLUDED_DIRS for part in path.parts):
            continue
        yield path


def _find_all_skills(config) -> list[dict]:
    """The contact point between filesystem and the rest of the agent.
    Every later operation (skill_view, curator, dashboard) starts here.
    """
    seen_names = set()
    skills = []
    for scan_dir in resolve_skill_dirs(config):
        for index in iter_skill_index_files(scan_dir):
            meta = parse_frontmatter(index.read_text())
            name = meta["name"]

            # Cross-directory dedupe: first directory wins on collision.
            # Useful when team-shared and user-private dirs both ship a
            # 'greet' skill — the user's local copy supersedes.
            if name in seen_names:
                continue

            # Filtered out via hermes_cli/skills_config.py:
            #   - explicitly disabled by the user
            #   - platform-mismatched (macos/linux tag)
            if is_disabled(name, config):
                continue
            if not platform_match(meta):
                continue

            seen_names.add(name)
            skills.append({
                "name": name,
                "category": _category_from_path(index, scan_dir),
                "description": meta["description"],
                "path": str(index),
                "metadata": meta.get("metadata", {}),
            })
    return skills


# Hermes registers EXACTLY ONE tool with the registry — `skill_view`.
# The model invokes it as skill_view(name='greet'), which returns the
# preprocessed body. This "progressive disclosure" trades 1 round trip
# for token savings: the agent doesn't see all skill bodies every turn,
# just the names + descriptions.
#
# Our Go mini-version chose the simpler "one tool per skill" design.
# Both are valid; pick based on skill count vs. context budget.
SKILL_VIEW_SCHEMA = {
    "type": "object",
    "properties": {
        "name": {"type": "string", "description": "The skill name (frontmatter `name`)."},
        "input": {"type": "string", "description": "Optional ${HERMES_SKILL_INPUT}."},
        "preprocess": {"type": "boolean", "default": True},
    },
    "required": ["name"],
}

registry.register(
    name="skill_view",
    toolset="skills",
    schema=SKILL_VIEW_SCHEMA,
    handler=_skill_view_with_bump,
    check_fn=check_skills_requirements,
    emoji="📚",
)


# ============================================================================
# agent/skill_preprocessing.py  --  template + inline shell expansion
# ============================================================================
#
# Imported from skills_tool.py via `from agent.skill_preprocessing import
# preprocess_skill_content`. Lives in agent/ rather than tools/ because the
# preprocessor is a pure function over a string and can be unit-tested
# without the full skill loader.

import re

_VAR_RE = re.compile(r"\$\{([A-Z_][A-Z0-9_]*)\}")
_SHELL_RE = re.compile(r"`([^`\n]+)`")


def preprocess_skill_content(body: str, env: dict) -> str:
    """The fenced-block-aware version. Splits markdown into segments,
    keeps ```...``` segments verbatim, runs var + shell expansion on
    everything else. Our Go mini omits this segmentation — that is the
    main fidelity gap to flag.
    """
    segments = split_by_fenced_blocks(body)
    out = []
    for seg in segments:
        if seg.is_fenced:
            out.append(seg.text)
            continue
        text = substitute_vars(seg.text, env)
        text = expand_inline_shell(text)
        out.append(text)
    return "".join(out)


def substitute_vars(s: str, env: dict) -> str:
    """${HERMES_*} placeholders. Unknown variables are left as literal so
    the model can see the gap; hermes prefers human-readable degradation
    over hard errors.
    """
    return _VAR_RE.sub(lambda m: env.get(m.group(1), m.group(0)), s)


def expand_inline_shell(s: str) -> str:
    """`cmd` → stdout of `bash -c cmd`. SECURITY: this is arbitrary code
    execution from a markdown file. Real hermes mitigates by gating skill
    installation behind explicit user action (`hermes skill install`).
    """
    return _SHELL_RE.sub(lambda m: _run_bash(m.group(1)), s)


def _run_bash(cmd: str) -> str:
    import subprocess
    try:
        return subprocess.check_output(["bash", "-c", cmd], text=True).rstrip("\n")
    except subprocess.CalledProcessError as e:
        # Surface the failure in-band so the LLM can see what went wrong.
        return f"(shell error: rc={e.returncode})"


# ============================================================================
# hermes_cli/curator.py  --  what "self-improving" actually means
# ============================================================================
#
# This file is the lifecycle manager for skills. It does NOT generate new
# skills automatically. It tracks usage and prunes stale ones — a quiet,
# crucial maintenance loop the agent runs on a schedule.

from datetime import datetime, timezone


def _idle_days(record: dict) -> Optional[int]:
    """Days since the skill's last activity (view / use / patch).
    Falls back to created_at so brand-new-but-never-used skills are
    eventually eligible for pruning.
    """
    ts = record.get("last_activity_at") or record.get("created_at")
    dt = datetime.fromisoformat(str(ts))
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return max(0, (datetime.now(timezone.utc) - dt).days)


# Three-bucket lifecycle:
#   active   — used recently (< stale_threshold days)
#   stale    — flagged for review (between stale_threshold and prune_threshold)
#   archived — moved to skills/.archive/, hidden from the loop
#
# Manual escape hatches: `pin` (never auto-archive), `rollback` (snapshot
# restore), `prune` (force-archive the bottom N% by idle days).
#
# THIS is hermes's self-improvement. Not automatic skill generation —
# automated skill *gardening*: keep the working set small, pruned, sharp.


# ============================================================================
# A reading map for going further
# ============================================================================
#
# - tools/skills_tool.py            full discovery + skill_view dispatch
# - agent/skill_preprocessing.py    template + shell expansion
# - hermes_cli/skills_config.py     enabled/disabled state, platform overrides
# - hermes_cli/curator.py           stale/archive lifecycle (the s06 hook)
# - hermes_cli/skills_hub.py        skills hub + agentskills.io integration
#
# Sessions in this course that revisit these files:
#   s06 (Plugin + Curator) — the curator becomes a plugin in our model
#   s05 (Memory provider)  — `last_activity_at` is a memory-provider concern
