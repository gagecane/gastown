#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.10"
# dependencies = [
#   "anthropic>=0.18.0",
#   "openai>=1.0.0",
#   "python-dotenv>=1.0.0",
#   "typer>=0.9.0",
# ]
# ///

"""
Generate a weekly Gas Town newsletter based on changelog, commits, and changes.

Usage:
    python generate-newsletter.py
    python generate-newsletter.py --model gpt-4o
    python generate-newsletter.py --since 2025-12-15
    python generate-newsletter.py --days 30
    python generate-newsletter.py --from-release v0.39 --to-release v0.48

Environment Variables:
    AI_MODEL        - The AI model to use (default: "claude-opus-4-1-20250805", e.g., "gpt-4o")
    ANTHROPIC_API_KEY or OPENAI_API_KEY - API credentials
    AUTO_COMMIT     - If "true", automatically commit and push the newsletter

Configuration File:
    .env            - Optional dotenv file in project root for API keys

Implementation is split across the ``scripts/newsletter`` package:
    - newsletter.changelog : git + CHANGELOG.md parsing
    - newsletter.pricing   : model pricing / cost helpers
    - newsletter.ai        : AI client + prompt + generation
"""

import os
import subprocess
import sys
from datetime import datetime, timedelta
from pathlib import Path
from typing import Optional

import typer

# Load environment variables from .env file if it exists
try:
    from dotenv import load_dotenv
    load_dotenv()
except ImportError:
    pass  # python-dotenv is optional, env vars can be set directly

# Make the newsletter helper package importable regardless of how this script
# is invoked (``uv run``, ``python``, or from a different cwd).
_SCRIPT_DIR = Path(__file__).resolve().parent
if str(_SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(_SCRIPT_DIR))

from newsletter.changelog import (  # noqa: E402
    check_git_branch,
    extract_breaking_changes,
    extract_new_commands,
    get_changelog_section,
    get_commits_since,
    get_previous_version,
    get_version_by_release,
)
from newsletter.pricing import (  # noqa: E402
    calculate_cost,
    detect_ai_provider,
    get_model_cost_info,
)
from newsletter.ai import (  # noqa: E402
    generate_with_claude,
    generate_with_openai,
    get_ai_client,
)


def generate_newsletter(
    model: Optional[str] = None,
    since_date: Optional[datetime] = None,
    until_date: Optional[datetime] = None,
    version: Optional[str] = None,
    from_version: Optional[str] = None,
    to_version: Optional[str] = None,
) -> tuple[str, str, datetime, datetime, int, int, float]:
    """Generate newsletter content.

    Returns:
        (newsletter_content, version_range, since_date, until_date,
         input_tokens, output_tokens, actual_cost)
    """
    # Determine the time period
    if since_date is None or until_date is None:
        # Use the current version as reference
        curr_version, curr_date = get_previous_version()

        if since_date is None:
            # Check if we should use last week or since last release
            week_ago = datetime.now() - timedelta(days=7)
            since_date = curr_date if curr_date > week_ago else week_ago

        if until_date is None:
            until_date = datetime.now()

        version = curr_version
    else:
        # since_date and until_date are provided
        if version is None:
            version = "Newsletter"

    # Get commits in the specified date range
    commits = get_commits_since(since_date)
    # Filter commits to be before until_date (both are naive datetimes)
    commits = [c for c in commits if c.get('date') is None or c['date'] <= until_date]

    # Get changelog for this version if applicable
    changelog = ""
    if version and version != "Newsletter":
        changelog = get_changelog_section(version)

    # Extract new commands and breaking changes if we have version info
    new_commands: list[dict] = []
    breaking_changes: list[dict] = []

    if from_version and to_version:
        # Extract from actual version range
        new_commands = extract_new_commands(from_version, to_version)
        if changelog:
            breaking_changes = extract_breaking_changes(changelog)
    elif version and version != "Newsletter":
        # Try to extract from changelog if version is available
        if changelog:
            breaking_changes = extract_breaking_changes(changelog)

    # Determine AI model
    if model is None:
        model = os.environ.get('AI_MODEL', 'claude-opus-4-1-20250805')

    # Detect provider and generate
    provider = detect_ai_provider(model)

    cost_info = get_model_cost_info(model)
    typer.echo(f"Using AI provider: {provider}")
    typer.echo(f"Model: {cost_info}")
    typer.echo(f"Period: {since_date.strftime('%Y-%m-%d')} to {until_date.strftime('%Y-%m-%d')}")
    typer.echo(f"Found {len(commits)} commits")
    if new_commands:
        typer.echo(f"Found {len(new_commands)} new commands")
    if breaking_changes:
        typer.echo(f"Found {len(breaking_changes)} breaking changes")

    client = get_ai_client(provider)

    if provider == 'anthropic':
        newsletter, input_tokens, output_tokens = generate_with_claude(
            client, commits, changelog, version, since_date, until_date,
            new_commands, breaking_changes, from_version, to_version,
        )
    else:
        newsletter, input_tokens, output_tokens = generate_with_openai(
            client, commits, changelog, version, since_date, until_date,
            new_commands, breaking_changes, from_version, to_version,
        )

    # Calculate actual cost
    actual_cost = calculate_cost(model, input_tokens, output_tokens)

    return newsletter, version, since_date, until_date, input_tokens, output_tokens, actual_cost


app = typer.Typer(help="Generate a weekly Gas Town newsletter based on changelog and commits")


def _warn_if_not_on_main() -> None:
    """Emit a warning and prompt for confirmation if the current branch isn't main."""
    current_branch = check_git_branch()
    if current_branch is None or current_branch == 'HEAD':
        typer.echo("WARNING: You are in detached HEAD state (not on any branch)", err=True)
        typer.echo("   Releases are made from the 'main' branch.", err=True)
        typer.echo("   The newsletter will be generated from the current commit's CHANGELOG.md.", err=True)
        typer.echo("   This may result in an outdated newsletter if you're not on the latest main.", err=True)
        if not typer.confirm("Continue anyway?"):
            typer.echo("Aborted.")
            raise typer.Exit(0)
    elif current_branch != 'main':
        typer.echo(f"WARNING: You are on branch '{current_branch}', not 'main'", err=True)
        typer.echo("   Releases are made from the 'main' branch.", err=True)
        typer.echo("   The newsletter will be generated from the current branch's CHANGELOG.md.", err=True)
        typer.echo("   This may result in an outdated newsletter if your branch is behind main.", err=True)
        if not typer.confirm("Continue anyway?"):
            typer.echo("Aborted.")
            raise typer.Exit(0)


def _resolve_time_period(
    from_release: Optional[str],
    to_release: Optional[str],
    days: Optional[int],
    since: Optional[str],
) -> tuple[Optional[datetime], Optional[datetime], Optional[str]]:
    """Resolve CLI flags into (since_date, until_date, version) values.

    Returns ``(None, None, None)`` when no time-related flags were supplied; in
    that case ``generate_newsletter`` applies its default (last week or since
    last release).
    """
    if from_release and to_release:
        start_ver, start_date = get_version_by_release(from_release)
        end_ver, end_date = get_version_by_release(to_release)
        return start_date, end_date, f"{start_ver} to {end_ver}"

    if from_release:
        start_ver, start_date = get_version_by_release(from_release)
        return start_date, datetime.now(), f"{start_ver} to present"

    if to_release:
        end_ver, end_date = get_version_by_release(to_release)
        return datetime(2000, 1, 1), end_date, f"up to {end_ver}"

    if days:
        until_date = datetime.now()
        since_date = until_date - timedelta(days=days)
        return since_date, until_date, f"Last {days} days"

    if since:
        until_date = datetime.now()
        if since.endswith('d'):
            num_days = int(since[:-1])
            since_date = until_date - timedelta(days=num_days)
            return since_date, until_date, f"Last {num_days} days"
        since_date = datetime.strptime(since, "%Y-%m-%d")
        return since_date, until_date, f"Since {since}"

    return None, None, None


def _maybe_auto_commit(output: str, version_str: str) -> None:
    """Commit and push the newsletter if AUTO_COMMIT=true."""
    if os.environ.get('AUTO_COMMIT', '').lower() != 'true':
        return
    subprocess.run(['git', 'add', output], check=True)
    commit_msg = f'docs: update newsletter for {version_str}'
    subprocess.run(['git', 'commit', '-m', commit_msg], check=True)
    subprocess.run(['git', 'push'], check=True)
    typer.echo("Committed and pushed newsletter")


@app.command()
def main(
    model: Optional[str] = typer.Option(None, "--model", "-m", help="AI model to use (default: claude-opus-4-1-20250805, e.g., gpt-4o, claude-sonnet-4-5-20250929)"),
    output: str = typer.Option("NEWSLETTER.md", "--output", "-o", help="Output file"),
    dry_run: bool = typer.Option(False, "--dry-run", help="Print to stdout instead of writing file"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip branch check warning"),
    since: Optional[str] = typer.Option(None, "--since", help="Start date (YYYY-MM-DD) or relative (e.g., 14d for last 14 days)"),
    days: Optional[int] = typer.Option(None, "--days", help="Generate for the last N days"),
    from_release: Optional[str] = typer.Option(None, "--from-release", help="Start from a specific release (e.g., v0.3.0 or 0.3.0)"),
    to_release: Optional[str] = typer.Option(None, "--to-release", help="End at a specific release (e.g., v0.4.0 or 0.4.0)"),
):
    """Generate a newsletter for a specified time period or release range.

    Examples:
        # Generate for last week (default)
        python generate-newsletter.py

        # Generate for last 30 days
        python generate-newsletter.py --days 30

        # Generate since a specific date
        python generate-newsletter.py --since 2025-12-15

        # Generate between two releases
        python generate-newsletter.py --from-release v0.3.0 --to-release v0.4.0
    """
    # Check git branch before proceeding
    if not force:
        _warn_if_not_on_main()

    try:
        since_date, until_date, version = _resolve_time_period(
            from_release, to_release, days, since,
        )

        newsletter, version_str, _start, _end, input_tokens, output_tokens, actual_cost = generate_newsletter(
            model=model,
            since_date=since_date,
            until_date=until_date,
            version=version,
            from_version=from_release,
            to_version=to_release,
        )

        # Display generation summary
        typer.echo("")
        typer.echo("=== Generation Summary ===")
        typer.echo(f"Tokens used: {input_tokens:,} input + {output_tokens:,} output = {input_tokens + output_tokens:,} total")
        typer.echo(f"Estimated cost: ${actual_cost:.4f}")

        if dry_run:
            typer.echo("")
            typer.echo(newsletter)
        else:
            Path(output).write_text(newsletter)
            typer.echo(f"Newsletter written to {output}")
            _maybe_auto_commit(output, version_str)

    except Exception as e:
        typer.echo(f"Error: {e}", err=True)
        raise typer.Exit(1)


if __name__ == "__main__":
    app()
