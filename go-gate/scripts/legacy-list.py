# Run as <gate>/scripts/legacy-list.py from the project root: the gate's
# install copies this file there. <gate>-legacy.txt, the files that failed
# the gate when it came into an existing project, only gets shorter (each
# gate's CLAUDE.md, "Legacy list"):
#
#   python3 -I <gate>/scripts/legacy-list.py <gate>
#
# Each line was on the list's first version in git, and no commit since
# deleted it, also when a later commit adds it again; and it names a file
# that exists. A list that turns up after <gate>/ was committed fails. It
# prints one `<list>:<line>: legacy-list: <how to fix>` line per problem and
# exits 1 when it prints one. Without the list it prints nothing. ts-gate
# runs it as a check of its own; py-gate's check.py and go-gate's check run
# it and report its lines with their own, which no off key and no line of
# the list hides.
import os
import subprocess
import sys

# A file of each gate's own folder: in HEAD, the gate was committed.
MARKER = {"ts-gate": "conf.cjs", "py-gate": "check.py", "go-gate": "check/main.go"}


def text(data: bytes) -> str:
    return data.decode("utf-8", "surrogateescape")


def git(*args: str) -> str | None:
    try:
        done = subprocess.run(["git", *args], capture_output=True, check=False)
    except OSError:
        return None
    return text(done.stdout) if done.returncode == 0 else None


def problems(gate: str) -> list[str]:
    listed = f"{gate}-legacy.txt"
    try:
        with open(listed, "rb") as f:
            rows = text(f.read()).split("\n")
    except OSError:
        return []
    first = (git("log", "--diff-filter=A", "--format=%H", "--", listed) or "").split()
    original = git("show", f"{first[-1]}:./{listed}") if first else None
    out: list[str] = []
    allowed: set[str] | None = None
    if original is None:
        if git("cat-file", "-e", f"HEAD:./{gate}/{MARKER[gate]}") is not None:
            out.append(f"{listed}:1: legacy-list: only {gate}'s first install writes this list, and {gate} was "
                       "in the project before it. Delete the file, and fix what the gate then reports")
    else:
        log = git("log", "--format=", "-p", "--unified=0", "--no-renames", "--", listed) or ""
        gone = {r[1:].strip() for r in log.split("\n") if r.startswith("-") and not r.startswith("--- ")}
        allowed = {r.strip() for r in original.split("\n")} - gone
    for n, row in enumerate(rows, 1):
        path = row.strip()
        if not path or path.startswith("#"):
            continue
        if allowed is not None and path not in allowed:
            out.append(f"{listed}:{n}: legacy-list: `{path}` was not on the list {gate}'s install wrote, and the "
                       "list only gets shorter. Delete the line, and fix what the gate then reports in the file")
        elif os.path.isdir(path):
            out.append(f"{listed}:{n}: legacy-list: `{path}` is a folder, and the list names files, one per "
                       "line. Delete the line, and fix what the gate then reports in the folder's files")
        elif not os.path.isfile(path):
            out.append(f"{listed}:{n}: legacy-list: `{path}` does not exist. Delete the line")
    return out


def main() -> int:
    if len(sys.argv) != 2 or sys.argv[1] not in MARKER:
        print(f"usage: python3 -I legacy-list.py {'|'.join(MARKER)}", file=sys.stderr)
        return 2
    out = problems(sys.argv[1])
    sys.stdout.reconfigure(errors="surrogateescape")
    for line in out:
        print(line)
    return 1 if out else 0


if __name__ == "__main__":
    sys.exit(main())
