"""Git repository and CHANGELOG.md parsing for the newsletter generator.

Responsibilities:
- Reading current git branch state
- Enumerating versions from CHANGELOG.md
- Fetching git commits in a date range
- Extracting changelog sections, new commands, and breaking changes
- Finding documentation excerpts for a command name
"""

from __future__ import annotations

import re
import subprocess
from datetime import datetime
from pathlib import Path
from typing import Optional


# Project root is two levels up from this file: scripts/newsletter/changelog.py -> repo root
_PROJECT_ROOT = Path(__file__).resolve().parent.parent.parent


def _changelog_path() -> Path:
    """Return the path to CHANGELOG.md at the project root."""
    return _PROJECT_ROOT / "CHANGELOG.md"


def check_git_branch() -> Optional[str]:
    """Check current git branch and return its name, or None if not in a repo."""
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--abbrev-ref", "HEAD"],
            capture_output=True,
            text=True,
            check=True,
        )
        return result.stdout.strip()
    except subprocess.CalledProcessError:
        # Not in a git repo or git not available
        return None


def get_all_versions() -> list[tuple[str, datetime]]:
    """Extract all versions and dates from CHANGELOG.md."""
    with open(_changelog_path()) as f:
        content = f.read()

    # Match version pattern like [0.3.0] - 2026-01-04
    pattern = r'## \[(\d+\.\d+\.\d+)\] - (\d{4}-\d{2}-\d{2})'
    matches = re.finditer(pattern, content)

    versions = []
    for match in matches:
        version = match.group(1)
        date_str = match.group(2)
        date = datetime.strptime(date_str, "%Y-%m-%d")
        versions.append((version, date))

    return versions


def get_version_by_release(version_str: str) -> tuple[str, datetime]:
    """Find a specific version by version string (e.g., 'v0.3.0' or '0.3.0')."""
    versions = get_all_versions()

    # Normalize the input (remove 'v' prefix if present)
    normalized = version_str.lstrip('v')

    for version, date in versions:
        if version == normalized:
            return version, date

    raise ValueError(f"Version {version_str} not found in CHANGELOG.md")


def get_previous_version() -> tuple[str, datetime]:
    """Extract the most recent version and date from CHANGELOG.md."""
    versions = get_all_versions()
    if versions:
        return versions[0]
    raise ValueError("Could not find any versions in CHANGELOG.md")


def get_commits_since(since_date: datetime) -> list[dict]:
    """Get git commits since the given date."""
    result = subprocess.run(
        [
            "git", "log",
            f"--since={since_date.strftime('%Y-%m-%d')}",
            "--oneline",
            "--format=%h|%s|%an|%ai",
        ],
        capture_output=True,
        text=True,
    )

    commits = []
    for line in result.stdout.strip().split('\n'):
        if not line:
            continue
        parts = line.split('|', 3)
        if len(parts) < 2:
            continue

        commit_date = None
        if len(parts) >= 4:
            try:
                # Parse as naive datetime (extract just the date part)
                date_str = parts[3].strip().split()[0]
                commit_date = datetime.strptime(date_str, "%Y-%m-%d")
            except (ValueError, AttributeError, IndexError):
                pass

        commits.append({
            'hash': parts[0],
            'subject': parts[1],
            'author': parts[2] if len(parts) > 2 else 'unknown',
            'date': commit_date,
        })

    return commits


def get_changelog_section(version: str) -> str:
    """Extract the changelog section for a specific version."""
    with open(_changelog_path()) as f:
        content = f.read()

    # Find the section for this version
    pattern = rf'## \[{re.escape(version)}\].*?(?=## \[|\Z)'
    match = re.search(pattern, content, re.DOTALL)

    if match:
        return match.group(0)

    return ""


def _git_diff_names_between(from_ver: str, to_ver: str, path: str) -> list[str]:
    """Return files changed between two version tags, trying both 'vX' and 'X' prefixes."""
    result = None
    for prefix in ('v', ''):
        result = subprocess.run(
            [
                "git", "diff",
                f"{prefix}{from_ver}..{prefix}{to_ver}",
                "--name-only",
                "--",
                path,
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode == 0:
            break

    if result is None or result.returncode != 0:
        return []

    return result.stdout.strip().split('\n') if result.stdout.strip() else []


def _parse_cobra_commands(content: str) -> list[tuple[str, str]]:
    """Parse cobra.Command definitions from Go source, returning (use, short) tuples."""
    # Look for patterns like: var someCmd = &cobra.Command{ ... Use: "commandname" ... Short: "description"
    cmd_pattern = (
        r'var\s+\w+Cmd\s*=\s*&cobra\.Command\{'
        r'[^}]*?Use:\s*["\']([^"\']+)["\']'
        r'[^}]*?Short:\s*["\']([^"\']+)["\']'
    )
    return [(m.group(1), m.group(2)) for m in re.finditer(cmd_pattern, content, re.DOTALL)]


def extract_new_commands(from_version: str, to_version: str) -> list[dict]:
    """Extract new commands added between versions by diffing the cmd/ directory.

    Returns list of dicts with: name, short, file.
    """
    try:
        # Normalize version strings (remove 'v' prefix if present)
        from_ver = from_version.lstrip('v')
        to_ver = to_version.lstrip('v')

        changed_files = _git_diff_names_between(from_ver, to_ver, "cmd/gt")
        if not changed_files:
            return []

        commands = []
        seen = set()

        for file_path in changed_files:
            if not file_path or file_path.endswith('_test.go'):
                continue

            try:
                full_path = _PROJECT_ROOT / file_path
                content = full_path.read_text()
            except (FileNotFoundError, IOError):
                continue

            for use, short in _parse_cobra_commands(content):
                # Extract just the command name (first word)
                cmd_name = use.split()[0]
                if cmd_name in seen:
                    continue
                seen.add(cmd_name)
                commands.append({
                    'name': cmd_name,
                    'short': short,
                    'file': file_path,
                })

        return sorted(commands, key=lambda x: x['name'])[:5]  # Top 5
    except Exception:
        return []


def extract_breaking_changes(changelog_section: str) -> list[dict]:
    """Extract breaking changes from a changelog section.

    Returns list of dicts with: title, description.
    """
    if not changelog_section:
        return []

    breaking: list[dict] = []

    # Look for "Breaking" subsection
    breaking_pattern = r'###\s+Breaking[^\n]*\n(.*?)(?=###|\Z)'
    breaking_match = re.search(breaking_pattern, changelog_section, re.IGNORECASE | re.DOTALL)
    if not breaking_match:
        return []

    breaking_text = breaking_match.group(1)
    # Split by top-level bullet points (not indented)
    # Matches: - **Title** followed by optional inline description or newline
    items = re.findall(
        r'^[-*]\s+\*\*([^*]+)\*\*\s*(?:-\s+)?([^\n]*)',
        breaking_text,
        re.MULTILINE,
    )
    for title, description in items[:5]:  # Top 5
        # Filter out empty descriptions or nested bullet continuations
        desc = description.strip()
        if desc and not desc.startswith('-'):
            breaking.append({
                'title': title.strip(),
                'description': desc,
            })

    return breaking


def find_docs_for_command(command_name: str) -> str:
    """Find documentation for a command in README or docs/.

    Returns a relevant excerpt or an empty string.
    """
    # Search in order of priority
    search_files = [_PROJECT_ROOT / "README.md"]

    # Add docs files
    docs_dir = _PROJECT_ROOT / "docs"
    if docs_dir.exists():
        search_files.extend(sorted(docs_dir.glob("*.md")))

    for file_path in search_files:
        try:
            content = file_path.read_text()
        except (FileNotFoundError, IOError):
            continue

        # Look for command mention with context
        pattern = rf'`{re.escape(command_name)}`|gt\s+{re.escape(command_name)}'
        if not re.search(pattern, content, re.IGNORECASE):
            continue

        # Find the paragraph/section containing this command
        matches = re.finditer(
            rf'[^\n]*{re.escape(command_name)}[^\n]*',
            content,
            re.IGNORECASE,
        )
        for match in matches:
            start = max(0, match.start() - 200)
            end = min(len(content), match.end() + 200)
            excerpt = content[start:end].strip()
            if len(excerpt) > 20:  # Filter out noise
                return excerpt

    return ""
