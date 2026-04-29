"""AI client selection, prompt building, and newsletter generation.

This module isolates the third-party AI SDK usage so the rest of the script
doesn't need to know whether we're hitting Anthropic or OpenAI.
"""

from __future__ import annotations

import os
from datetime import datetime
from typing import Optional


# Newsletter prompt template
NEWSLETTER_PROMPT_TEMPLATE = """Generate a Gas Town newsletter covering the period from {since_date} to {until_date}.

Reporting Period:
- Date Range: {since_date} to {until_date}
- {version_info}

## Recent Commits (last 50)
{commit_summary}

## Changelog for {version}
{changelog}

{new_commands_text}

{breaking_text}

Please write a newsletter that includes:
1. **Header with release versions** - ALWAYS state the release versions AND beginning & end of reporting range at the top: "v0.3.0 - v0.4.0, 2026-Jan-10 to 2026-Jan-17"
2. A brief intro about the release period
3. **New Commands & Options** section - describe what each does, why users should care, and show brief example
4. **Breaking Changes** section (if any) - explain what changed and why, migration path if applicable
5. Major Features & Bug Fixes (3-5 most significant)
6. Minor improvements/changes
7. Getting started section
8. Link to full changelog.md and GH release page

Default to writing in narrative paragraphs, use bullets sparingly.
Mention dates, significant commit hashes, and link to the relevant docs adjacent to their sections.
Keep it 500 to 1000 words.
Use emoji where appropriate.
Format in Markdown.
"""


def get_ai_client(provider: str):
    """Instantiate an AI client for the given provider.

    Requires ANTHROPIC_API_KEY or OPENAI_API_KEY in the environment. Imports
    the SDK lazily so callers that only use one provider don't need both.
    """
    if provider == 'anthropic':
        api_key = os.environ.get('ANTHROPIC_API_KEY')
        if not api_key:
            raise ValueError("ANTHROPIC_API_KEY environment variable not set")
        from anthropic import Anthropic  # lazy import
        return Anthropic(api_key=api_key)

    if provider == 'openai':
        api_key = os.environ.get('OPENAI_API_KEY')
        if not api_key:
            raise ValueError("OPENAI_API_KEY environment variable not set")
        from openai import OpenAI  # lazy import
        return OpenAI(api_key=api_key)

    raise ValueError(f"Unknown provider: {provider}")


def _format_version_info(
    version: str,
    from_version: Optional[str],
    to_version: Optional[str],
) -> str:
    """Build the 'version_info' header line for the prompt."""
    if from_version and to_version:
        return f"Release Range: v{from_version} to v{to_version}"
    if from_version:
        return f"Starting from: v{from_version}"
    if version and version != "Newsletter":
        return f"Version: {version}"
    return f"Period: {version}"


def build_newsletter_prompt(
    commits: list[dict],
    changelog: str,
    version: str,
    since_date: datetime,
    until_date: Optional[datetime] = None,
    new_commands: Optional[list[dict]] = None,
    breaking_changes: Optional[list[dict]] = None,
    from_version: Optional[str] = None,
    to_version: Optional[str] = None,
) -> str:
    """Build the newsletter prompt from its components."""
    if until_date is None:
        until_date = datetime.now()

    commit_summary = "\n".join(f"- {c['subject']}" for c in commits[:50])

    new_commands_text = ""
    if new_commands:
        new_commands_text = "## New Commands & Options\n"
        for cmd in new_commands:
            new_commands_text += f"- **{cmd['name']}** - {cmd['short']}\n"

    breaking_text = ""
    if breaking_changes:
        breaking_text = "## Breaking Changes\n"
        for change in breaking_changes:
            breaking_text += f"- **{change['title']}** - {change['description']}\n"

    version_info = _format_version_info(version, from_version, to_version)

    since_str = since_date.strftime('%B %d, %Y')
    until_str = until_date.strftime('%B %d, %Y')

    return NEWSLETTER_PROMPT_TEMPLATE.format(
        since_date=since_str,
        until_date=until_str,
        version_info=version_info,
        commit_summary=commit_summary,
        version=version,
        changelog=changelog[:3000],
        new_commands_text=new_commands_text,
        breaking_text=breaking_text,
    )


def generate_with_claude(
    client,
    commits: list[dict],
    changelog: str,
    version: str,
    since_date: datetime,
    until_date: Optional[datetime] = None,
    new_commands: Optional[list[dict]] = None,
    breaking_changes: Optional[list[dict]] = None,
    from_version: Optional[str] = None,
    to_version: Optional[str] = None,
) -> tuple[str, int, int]:
    """Generate newsletter using Claude. Returns (text, input_tokens, output_tokens)."""
    prompt = build_newsletter_prompt(
        commits, changelog, version, since_date, until_date,
        new_commands, breaking_changes, from_version, to_version,
    )

    response = client.messages.create(
        model="claude-opus-4-1-20250805",
        max_tokens=4000,
        messages=[{"role": "user", "content": prompt}],
    )

    input_tokens = response.usage.input_tokens
    output_tokens = response.usage.output_tokens
    return response.content[0].text, input_tokens, output_tokens


def generate_with_openai(
    client,
    commits: list[dict],
    changelog: str,
    version: str,
    since_date: datetime,
    until_date: Optional[datetime] = None,
    new_commands: Optional[list[dict]] = None,
    breaking_changes: Optional[list[dict]] = None,
    from_version: Optional[str] = None,
    to_version: Optional[str] = None,
) -> tuple[str, int, int]:
    """Generate newsletter using OpenAI. Returns (text, input_tokens, output_tokens)."""
    prompt = build_newsletter_prompt(
        commits, changelog, version, since_date, until_date,
        new_commands, breaking_changes, from_version, to_version,
    )

    response = client.chat.completions.create(
        model="gpt-4o",
        messages=[{"role": "user", "content": prompt}],
        max_tokens=4000,
    )

    input_tokens = response.usage.prompt_tokens
    output_tokens = response.usage.completion_tokens
    return response.choices[0].message.content, input_tokens, output_tokens
