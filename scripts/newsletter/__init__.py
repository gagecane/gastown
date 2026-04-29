"""Helper modules for the newsletter generator script.

This package is imported by scripts/generate-newsletter.py. It is split into
three submodules by concern:

- changelog: git + CHANGELOG.md parsing (versions, commits, commands, breaking changes)
- pricing:   model pricing / cost calculation helpers
- ai:        AI client selection, prompt building, and newsletter generation
"""
