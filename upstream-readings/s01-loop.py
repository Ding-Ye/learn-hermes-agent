# Upstream source reading for s01 · Minimum agent loop
#
# Source: NousResearch/hermes-agent · hermes_cli/main.py
# License: see https://github.com/NousResearch/hermes-agent/blob/main/LICENSE
#
# This file is a *reading aid*, not the original source. It excerpts the
# minimum loop shape from hermes-agent and simplifies away orthogonal
# concerns (signal handling, telemetry, branching) so a learner can compare
# it line-by-line with our Go mini-loop in agents/s01-loop/loop.go.
#
# Annotations are inline. Line numbers in this file are NOT line numbers in
# upstream hermes-agent — they are local. Each block points back to the
# upstream module path in its leading comment.

# --- excerpted from hermes_cli/main.py (the session loop) ----------------

async def _run_session(session, provider, tools):
    """One agent session: repeatedly call the model, dispatch on stop_reason.

    `session` carries messages + system prompt + token totals; we will
    rebuild it ourselves in s04. For now treat it as just a typed wrapper
    around the messages list.
    """
    while True:
        # (1) Call the LLM with the running message list.
        resp = await provider.create_message(
            session.messages,
            session.system_prompt,
            tools=tools,
        )

        # (2) Persist immediately. hermes records token usage and the
        # assistant turn to disk *every iteration* — survival of `Ctrl-C`
        # is one of its design goals. We will introduce this in s04.
        session.record_usage(resp.usage)
        session.add_assistant(resp.content)
        await session.persist()

        # (3) Branch on stop_reason. Identical shape to our Go version.
        if resp.stop_reason == "end_turn":
            return resp

        if resp.stop_reason == "tool_use":
            results = await execute_tool_calls(resp.content, tools)
            session.add_user_tool_results(results)
            continue  # back to the top of the loop

        # (4) hermes treats unknown stop_reasons as *recoverable events*
        # that a supervisor decides what to do with — not panics.
        # Our Go mini just errors out; production code would not.
        raise UnexpectedStop(resp.stop_reason)


# --- excerpted from tools/registry.py (tool dispatch) --------------------

async def execute_tool_calls(content_blocks, registry):
    """For every tool_use block in the assistant turn, run the named tool
    and emit a tool_result block carrying the same tool_use_id.

    Hermes does this in parallel via asyncio.gather; our Go mini executes
    serially in s01 and parallelises in a later session.
    """
    tasks = []
    for block in content_blocks:
        if block.type != "tool_use":
            continue
        tool = registry.get(block.name)  # raises if unknown
        tasks.append(_run_one(tool, block))
    return await asyncio.gather(*tasks)


async def _run_one(tool, block):
    try:
        output = await tool.execute(block.input)
    except Exception as e:
        # The tool raising is NOT a loop-level error: we surface it back
        # as a tool_result so the model can see the failure and retry.
        output = f"tool error: {e}"
    return {
        "type": "tool_result",
        "tool_use_id": block.id,
        "content": output,
    }


# --- not in upstream: a guide to reading further -------------------------
#
# - hermes_cli/main.py        — the entry point + session loop
# - agent/providers/anthropic.py — Provider implementations
# - tools/registry.py         — tool dispatch + parallel execution
# - tools/builtin/bash.py     — equivalent of our BashTool
#
# Each of these will be revisited in a later session in this course.
