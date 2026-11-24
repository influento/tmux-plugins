# Run by each gate's Stop hook as <gate>/scripts/worked-in.py: the gate's
# install copies this file there. Reads the hook's input on stdin and prints
# the checkouts this session worked in, one per line, so the hook checks
# those and leaves alone the work of another session in the same project.
# What and why: ecosystems/CLAUDE.md in the workshop repo, "Whose work".
#
#   python3 -I worked-in.py <gate> [--why]
#
# It prints the folder of each gate that judges a changed file of the
# session's (the branch since the default branch, and the untracked ones): a
# path that its Edit, Write, MultiEdit or NotebookEdit named, or a file whose
# mtime falls inside one of its other tool calls that ran in the checkout.
# That folder is the nearest one above the file, within its checkout, that
# has <gate>/scripts/gate.sh: the checkout itself, or the project's folder
# when the project sits in a subfolder of its repository. Only a file the
# gate's --local takes as a change counts (CODE): notes, docs and data never
# make a checkout the session's, so a session that only writes those stops. A
# file under a .claude/ counts only when a gate reads it. A subagent's
# transcript counts as the session's. When the input names no readable
# transcript, it prints the project directory: the hook then checks as it
# did before.
import glob
import json
import os
import re
import subprocess
import sys
import time
from datetime import datetime

EDITS = {"Edit": "file_path", "Write": "file_path", "MultiEdit": "file_path", "NotebookEdit": "notebook_path"}
# Tools that never write a file of the project.
READS = {
    "Read", "Grep", "Glob", "LS", "WebFetch", "WebSearch", "ToolSearch", "AskUserQuestion", "SendMessage",
    "ListAgents", "TodoWrite", "TaskCreate", "TaskUpdate", "TaskList", "TaskGet", "TaskOutput", "Skill",
    "ExitPlanMode", "EnterPlanMode", "ScheduleWakeup", "Agent", "Task",
}
# The files each gate's --local takes as a change (its gate.sh): its
# languages by suffix, and by the end of the path its manifest and config
# files, its legacy list, its own folder, the settings and test data. A gate
# not listed here counts every file.
CODE = {
    "ts-gate": (
        {".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"},
        r"package(-lock)?\.json|tsconfig[^/]*\.json|biome\.jsonc?|eslint\.config\.[a-z]+|vite(st)?\.[a-z.]+"
        r"|ts-gate-legacy\.txt|ts-gate/.*|\.claude/eslint/.*|\.claude/workshop\.conf",
    ),
    "py-gate": (
        {".py", ".pyi"},
        r"pyproject\.toml|uv\.lock|\.python-version|ruff\.toml|pyrightconfig\.json|pytest\.toml"
        r"|py-gate-legacy\.txt|py-gate/.*|\.claude/workshop\.conf",
    ),
    "go-gate": (
        {".go"},
        r"go\.(mod|sum|work|work\.sum)|go-gate-legacy\.txt|go-gate/.*|testdata/.*|vendor/.*|\.claude/workshop\.conf",
    ),
}
# A clock step between the transcript's stamp and the file system's.
SLACK = 2.0
PATH_IN_COMMAND = re.compile(r"""(?:^|[\s'"=:(])((?:/|~/)[^\s'";|&<>()`$]+)""")
LEADING_CD = re.compile(r"""^\s*cd\s+(?:'([^']+)'|"([^"]+)"|([^\s;&|]+))\s*(?:&&|;|$)""")
# .claude/ holds the agent's settings and memory, never the project's code:
# the gates read only workshop.conf there, and ts-gate the rules in eslint/.
# In a project in a subfolder of its repository, it is under that folder.
AGENT_FILE = re.compile(r"(?:^|/)\.claude/(?!workshop\.conf$|eslint/)")


def stamp(text):
    try:
        return datetime.fromisoformat(text.replace("Z", "+00:00")).timestamp()
    except (AttributeError, ValueError):
        return None


def git(cwd, *args):
    try:
        return subprocess.run(["git", "-C", cwd, *args], capture_output=True, text=True, check=False).stdout
    except OSError:
        return ""


_tops = {}


def toplevel(path):
    d = path
    while d and not os.path.isdir(d):
        d = os.path.dirname(d)
    if not d:
        return None
    if d not in _tops:
        _tops[d] = git(d, "rev-parse", "--show-toplevel").strip() or None
    return _tops[d]


def expand(path, cwd):
    path = os.path.expanduser(path)
    return os.path.normpath(path if os.path.isabs(path) else os.path.join(cwd, path))


def calls(transcript):
    """Yield (name, input, start, end, cwd) for each tool call of one transcript."""
    uses, ends = {}, {}
    with open(transcript, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            try:
                entry = json.loads(line)
            except ValueError:
                continue
            content = (entry.get("message") or {}).get("content")
            if not isinstance(content, list):
                continue
            at = stamp(entry.get("timestamp"))
            for block in content:
                if not isinstance(block, dict):
                    continue
                if block.get("type") == "tool_use" and at is not None:
                    uses[block.get("id")] = (block.get("name"), block.get("input") or {}, at, entry.get("cwd") or "")
                elif block.get("type") == "tool_result" and at is not None:
                    ends[block.get("tool_use_id")] = at
    now = time.time()
    for key, (name, args, start, cwd) in uses.items():
        end = ends.get(key, now)
        # A background command goes on after its result.
        if name == "Bash" and args.get("run_in_background"):
            end = now
        yield name, args, start, end, cwd


def dirs_of(name, args, cwd):
    """The directories a tool call may have written in."""
    if name != "Bash":
        return [cwd] if cwd else []
    command = str(args.get("command") or "")
    out = []
    m = LEADING_CD.match(command)
    if m and cwd:
        out.append(expand(next(g for g in m.groups() if g), cwd))
    elif cwd:
        out.append(cwd)
    out += [expand(p, cwd or "/") for p in PATH_IN_COMMAND.findall(command)]
    return out


def default_base(top):
    # Before the first commit, the empty tree: every file in the index is a change.
    if not git(top, "rev-parse", "-q", "--verify", "HEAD").strip():
        return git(top, "hash-object", "-t", "tree", os.devnull).strip()
    head = git(top, "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD").strip()
    for b in () if head else ("main", "master"):
        if subprocess.run(["git", "-C", top, "show-ref", "-q", "--verify", f"refs/heads/{b}"], check=False).returncode == 0:
            head = b
            break
    base = git(top, "merge-base", "HEAD", head).strip() if head else ""
    return base or "HEAD"


def judged(gate, path):
    if gate not in CODE:
        return True
    suffixes, ends = CODE[gate]
    return os.path.splitext(path)[1] in suffixes or re.search(rf"(?:^|/)(?:{ends})$", path) is not None


def changed(top):
    base = default_base(top)
    names = git(top, "diff", "--name-only", "--diff-filter=ACMRD", base).splitlines()
    names += git(top, "ls-files", "--others", "--exclude-standard").splitlines()
    return sorted({os.path.join(top, n) for n in names if n and not n.endswith("/") and not AGENT_FILE.search(n)})


def gate_folder(gate, top, path):
    """The folder of the gate that judges a file of the checkout top: the nearest one above the file, up to top."""
    d = os.path.dirname(path)
    while not os.path.isfile(os.path.join(d, gate, "scripts", "gate.sh")):
        if len(d) <= len(top):
            return None
        d = os.path.dirname(d)
    return d


def mtime(path):
    d = path
    while d and not os.path.lexists(d):
        d = os.path.dirname(d)
    try:
        return os.lstat(d).st_mtime if d else None
    except OSError:
        return None


def main():
    gate = sys.argv[1]
    why = "--why" in sys.argv[2:]
    try:
        hook = json.load(sys.stdin)
    except ValueError:
        hook = {}
    project = os.environ.get("CLAUDE_PROJECT_DIR") or hook.get("cwd") or os.getcwd()
    transcript = hook.get("transcript_path") or ""
    if not os.path.isfile(transcript):
        print(project)
        return
    transcripts = [transcript, *sorted(glob.glob(os.path.join(transcript[: -len(".jsonl")], "subagents", "*.jsonl")))]

    named = set()  # paths an edit tool named
    windows = {}  # checkout -> [(start, end)]
    for t in transcripts:
        try:
            found = list(calls(t))
        except OSError:
            continue
        for name, args, start, end, cwd in found:
            if name in EDITS:
                path = args.get(EDITS[name])
                if isinstance(path, str) and path:
                    named.add(expand(path, cwd or project))
                continue
            if name in READS:
                continue
            for d in dirs_of(name, args, cwd):
                top = toplevel(d)
                if top:
                    windows.setdefault(top, []).append((start - SLACK, end + SLACK))
    tops = set(windows)
    tops |= {t for t in (toplevel(p) for p in named) if t}

    found = {}  # gate folder -> its first file of the session's, and why
    for top in sorted(tops):
        spans = windows.get(top, [])
        for f in (f for f in changed(top) if judged(gate, f)):
            folder = gate_folder(gate, top, f)
            if folder is None or folder in found:
                continue
            if f in named:
                reason = "edited"
            else:
                m = mtime(f)
                if m is None or not any(a <= m <= b for a, b in spans):
                    continue
                reason = "written during a tool call"
            found[folder] = f"{os.path.relpath(f, folder)}: {reason}"
    for folder in sorted(found):
        print(f"{folder}\t{found[folder]}" if why else folder)


if __name__ == "__main__":
    main()
