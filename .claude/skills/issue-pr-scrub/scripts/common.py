"""Shared helpers for the issue-pr-scrub pipeline.

Stdlib-only (Python 3.11+: tomllib, sqlite3, subprocess, argparse). No pip
installs, so the skill folder is portable: copy it into any OSS project and
point --repo / --config at the new target.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import tomllib
from datetime import datetime, timezone
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent


# --------------------------------------------------------------------------- #
# config + repo                                                               #
# --------------------------------------------------------------------------- #
def parse_repo(slug: str) -> tuple[str, str]:
    slug = slug.strip().removeprefix("https://github.com/").strip("/")
    if slug.count("/") != 1 or not all(slug.split("/")):
        die(f"--repo must be owner/name, got {slug!r}")
    owner, repo = slug.split("/")
    return owner, repo


def find_config(repo_slug: str, explicit: str | None) -> Path:
    """Resolve a config file: explicit > per-repo > example default."""
    if explicit:
        p = Path(explicit).expanduser()
        if not p.exists():
            die(f"--config {p} not found")
        return p
    owner, repo = parse_repo(repo_slug)
    per_repo = SCRIPT_DIR / f"config.{owner}__{repo}.toml"
    if per_repo.exists():
        return per_repo
    return SCRIPT_DIR / "config.example.toml"


def load_config(path: Path) -> dict:
    with path.open("rb") as fh:
        return tomllib.load(fh)


def expand(path_template: str, owner: str, repo: str) -> Path:
    return Path(
        os.path.expanduser(path_template.format(owner=owner, repo=repo))
    )


def expand_path(p: str) -> Path:
    path = Path(p).expanduser()
    if not path.exists():
        die(f"file not found: {path}")
    return path


def workdir(cfg: dict, owner: str, repo: str) -> Path:
    tmpl = cfg.get("paths", {}).get(
        "workdir", "~/.cache/issue-pr-scrub/{owner}__{repo}"
    )
    d = expand(tmpl, owner, repo)
    d.mkdir(parents=True, exist_ok=True)
    return d


def gitcrawl_db(cfg: dict) -> Path:
    # Honor gitcrawl's own override first, then config, then default.
    env = os.environ.get("GITCRAWL_DB_PATH")
    if env:
        return Path(env).expanduser()
    tmpl = cfg.get("paths", {}).get(
        "gitcrawl_db", "~/.config/gitcrawl/gitcrawl.db"
    )
    return Path(os.path.expanduser(tmpl))


# --------------------------------------------------------------------------- #
# jsonl + state                                                               #
# --------------------------------------------------------------------------- #
def write_jsonl(path: Path, rows) -> int:
    n = 0
    with path.open("w", encoding="utf-8") as fh:
        for row in rows:
            fh.write(json.dumps(row, ensure_ascii=False) + "\n")
            n += 1
    return n


def read_jsonl(path: Path):
    if not path.exists():
        return
    with path.open(encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if line:
                yield json.loads(line)


def load_state(wd: Path) -> dict:
    p = wd / "state.json"
    return json.loads(p.read_text()) if p.exists() else {}


def save_state(wd: Path, state: dict) -> None:
    (wd / "state.json").write_text(json.dumps(state, indent=2))


# --------------------------------------------------------------------------- #
# ledger (apply-time dedup, append-only)                                       #
# --------------------------------------------------------------------------- #
def ledger_path(wd: Path) -> Path:
    return wd / "ledger.jsonl"


def ledger_done(wd: Path) -> set[tuple[int, str]]:
    """Set of (number, action) already applied successfully."""
    done = set()
    for row in read_jsonl(ledger_path(wd)):
        if row.get("status") == "ok":
            done.add((int(row["number"]), row["action"]))
    return done


def ledger_append(wd: Path, entry: dict) -> None:
    entry.setdefault("ts", now_iso())
    with ledger_path(wd).open("a", encoding="utf-8") as fh:
        fh.write(json.dumps(entry, ensure_ascii=False) + "\n")


# --------------------------------------------------------------------------- #
# gh + git wrappers                                                            #
# --------------------------------------------------------------------------- #
def run(cmd: list[str], check: bool = True, capture: bool = True) -> subprocess.CompletedProcess:
    return subprocess.run(
        cmd,
        check=check,
        text=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
    )


def gh_json(args: list[str]):
    """Run `gh <args>` expecting JSON on stdout."""
    cp = run(["gh", *args])
    return json.loads(cp.stdout)


def require_gh_auth() -> None:
    cp = run(["gh", "auth", "status"], check=False)
    if cp.returncode != 0:
        die("gh is not authenticated. Run `gh auth login` first.\n" + (cp.stderr or ""))


def have(binary: str) -> bool:
    from shutil import which

    return which(binary) is not None


# --------------------------------------------------------------------------- #
# time                                                                        #
# --------------------------------------------------------------------------- #
def now_iso() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def parse_ts(value: str | None):
    if not value:
        return None
    v = value.strip().replace("Z", "+00:00")
    try:
        dt = datetime.fromisoformat(v)
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt


def days_since(value: str | None) -> float | None:
    dt = parse_ts(value)
    if dt is None:
        return None
    return (datetime.now(timezone.utc) - dt).total_seconds() / 86400.0


# --------------------------------------------------------------------------- #
# misc                                                                        #
# --------------------------------------------------------------------------- #
def die(msg: str, code: int = 1):
    print(f"error: {msg}", file=sys.stderr)
    sys.exit(code)


def info(msg: str):
    print(msg, file=sys.stderr)


def base_argparser(description: str) -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(description=description)
    ap.add_argument("--repo", required=True, help="owner/name")
    ap.add_argument("--config", default=None, help="path to a config toml")
    return ap


def bootstrap(args) -> dict:
    """Common setup: resolve repo, config, workdir. Returns a ctx dict."""
    owner, repo = parse_repo(args.repo)
    cfg_path = find_config(args.repo, args.config)
    cfg = load_config(cfg_path)
    wd = workdir(cfg, owner, repo)
    return {
        "owner": owner,
        "repo": repo,
        "slug": f"{owner}/{repo}",
        "cfg": cfg,
        "cfg_path": cfg_path,
        "wd": wd,
    }
