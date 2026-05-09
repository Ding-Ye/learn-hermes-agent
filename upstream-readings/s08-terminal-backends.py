# Upstream source reading for s08 · Terminal backend factory
#
# Source: NousResearch/hermes-agent · tools/terminal_tool.py
# License: see https://github.com/NousResearch/hermes-agent/blob/main/LICENSE
#
# Teaching excerpt focused on the factory + uniform output dict.

import abc
import os
import asyncio
import subprocess


# ============================================================================
# tools/terminal_tool.py  --  base class + factory
# ============================================================================

class _Environment(abc.ABC):
    """Base class for every execution environment hermes knows about.

    Concrete impls differ wildly under the hood:
      - LocalEnvironment       -> subprocess on the host
      - DockerEnvironment      -> docker exec into a long-running container
      - SSHEnvironment         -> paramiko channel multiplexing
      - ModalEnvironment       -> modal.Function over the cloud
      - SingularityEnvironment -> HPC clusters
      - DaytonaEnvironment     -> sandboxed dev workspace
      - VercelEnvironment      -> per-tenant edge sandbox

    The output dict shape is invariant across all of them, so the LLM's
    `terminal` tool sees one schema regardless of backend.
    """

    @abc.abstractmethod
    async def execute(self, command: str, *, timeout: float = 60.0,
                      cwd: str | None = None) -> dict:
        """Returns:
           {"stdout": str, "stderr": str,
            "exit_code": int, "duration_seconds": float,
            "backend": str, "cwd": str | None}
        """

    @abc.abstractmethod
    async def close(self): ...


def _create_environment(config: dict) -> _Environment:
    """Factory keyed on config["TERMINAL_ENV"] (or env var)."""
    kind = (config.get("TERMINAL_ENV") or os.getenv("TERMINAL_ENV", "local"))
    if kind == "local":        return LocalEnvironment()
    if kind == "docker":       return DockerEnvironment(config)
    if kind == "ssh":          return SSHEnvironment(config)
    if kind == "modal":        return ModalEnvironment(config)
    if kind == "singularity":  return SingularityEnvironment(config)
    if kind == "daytona":      return DaytonaEnvironment(config)
    if kind == "vercel":       return VercelEnvironment(config)
    raise ValueError(f"unknown TERMINAL_ENV: {kind}")


# ============================================================================
# Concrete: DockerEnvironment (the most interesting non-trivial one)
# ============================================================================

class DockerEnvironment(_Environment):
    """The KEY DIFFERENCE from our Go mini: hermes keeps a long-running
    container alive for the whole session and uses `docker exec` for
    follow-on commands. Our mini does `docker run --rm` per command —
    simpler to read, but slow and loses cwd/state between commands.

    Container lifecycle:
      - start on first execute() (or eager start at session_start)
      - re-used for every subsequent execute()
      - reaper removes containers that have been idle for N minutes
      - close() docker stops + removes
    """

    IDLE_REAP_MINUTES = 10

    def __init__(self, config: dict):
        self._image = config.get("DOCKER_IMAGE", "alpine:3.19")
        self._container_id: str | None = None
        self._last_active: float = 0.0
        # ... volume mounts, network, resource limits in real config

    async def _ensure_running(self) -> str:
        if self._container_id is None:
            # docker run -d --restart=no <image> sleep infinity
            self._container_id = await self._docker_run_detached()
        return self._container_id

    async def execute(self, command, *, timeout=60.0, cwd=None):
        cid = await self._ensure_running()
        args = ["docker", "exec"]
        if cwd:
            args += ["-w", cwd]
        args += [cid, "bash", "-c", command]
        result = await _run_subprocess(args, timeout)
        result["backend"] = f"docker:{self._image}"
        result["cwd"] = cwd
        self._last_active = _now()
        return result

    async def close(self):
        if self._container_id:
            await _run_subprocess(["docker", "rm", "-f", self._container_id], 10.0)
            self._container_id = None


# ============================================================================
# Shared executor — all backends bottleneck through here
# ============================================================================

async def _run_subprocess(args: list[str], timeout: float) -> dict:
    """All backends end up calling this. Output dict shape is the contract."""
    proc = await asyncio.create_subprocess_exec(
        *args,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    try:
        stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=timeout)
        exit_code = proc.returncode
    except asyncio.TimeoutError:
        proc.kill()
        await proc.wait()
        return {"stdout": "", "stderr": "TIMEOUT", "exit_code": 124,
                "duration_seconds": timeout, "timed_out": True}
    return {
        "stdout": stdout.decode("utf-8", errors="replace"),
        "stderr": stderr.decode("utf-8", errors="replace"),
        "exit_code": exit_code,
    }


# ============================================================================
# A reading map
# ============================================================================
#
# - tools/terminal_tool.py        base class + factory + concrete envs
# - tools/terminal_tool/docker_env.py
# - tools/terminal_tool/ssh_env.py
# - tools/terminal_tool/modal_env.py
# - tools/terminal_tool/daytona_env.py
# - hermes_cli/config.py          where TERMINAL_ENV gets parsed
#
# Sessions in this course that revisit these files:
#   s09 (Multi-process) — the Gateway can target a different backend per
#                          source (Telegram users get Docker, CLI gets local)
```
