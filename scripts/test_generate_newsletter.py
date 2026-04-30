#!/usr/bin/env python3
"""Unit tests for scripts/generate-newsletter.py.

The script under test has a hyphen in its name and a top-level `import typer`,
plus optional imports of `anthropic`, `openai`, and `python-dotenv`. To keep
this test suite runnable with only the Python standard library (and with
network calls fully mocked), we inject lightweight stubs for those third-party
modules into `sys.modules` *before* loading the script via `importlib`.

Run with:

    python3 -m pytest scripts/test_generate_newsletter.py
    # or
    python3 -m unittest scripts.test_generate_newsletter

The tests never hit the network, never shell out to `git`, and never touch
the real filesystem outside of temporary directories.
"""

from __future__ import annotations

import importlib.util
import io
import os
import sys
import types
import unittest
from datetime import datetime, timedelta
from pathlib import Path
from unittest import mock


# ---------------------------------------------------------------------------
# Stub external dependencies BEFORE importing the script under test.
# ---------------------------------------------------------------------------

def _install_stub_modules() -> None:
    """Register minimal stubs for typer/anthropic/openai/dotenv in sys.modules.

    Only installs a stub if the real module is not already importable, so if
    a developer has the real packages locally the tests still work.
    """

    # --- typer ---------------------------------------------------------------
    if "typer" not in sys.modules:
        typer_stub = types.ModuleType("typer")

        class _Typer:
            def __init__(self, *args, **kwargs):
                self.help = kwargs.get("help", "")

            def command(self, *a, **k):
                def deco(fn):
                    return fn
                return deco

            def __call__(self, *a, **k):  # pragma: no cover - not invoked
                return None

        def _option(default=None, *args, **kwargs):
            return default

        class _Exit(SystemExit):
            def __init__(self, code: int = 0):
                super().__init__(code)
                self.exit_code = code

        typer_stub.Typer = _Typer
        typer_stub.Option = _option
        typer_stub.echo = lambda *a, **k: None
        typer_stub.Exit = _Exit
        typer_stub.confirm = lambda *a, **k: True
        sys.modules["typer"] = typer_stub

    # --- anthropic -----------------------------------------------------------
    if "anthropic" not in sys.modules:
        anthropic_stub = types.ModuleType("anthropic")

        class _Anthropic:
            def __init__(self, *a, **k):
                self.messages = mock.MagicMock()

        anthropic_stub.Anthropic = _Anthropic
        sys.modules["anthropic"] = anthropic_stub

    # --- openai --------------------------------------------------------------
    if "openai" not in sys.modules:
        openai_stub = types.ModuleType("openai")

        class _OpenAI:
            def __init__(self, *a, **k):
                self.chat = mock.MagicMock()

        openai_stub.OpenAI = _OpenAI
        sys.modules["openai"] = openai_stub

    # --- dotenv --------------------------------------------------------------
    if "dotenv" not in sys.modules:
        dotenv_stub = types.ModuleType("dotenv")
        dotenv_stub.load_dotenv = lambda *a, **k: False
        sys.modules["dotenv"] = dotenv_stub


_install_stub_modules()


def _load_script_module():
    """Load scripts/generate-newsletter.py as a Python module.

    The hyphenated filename prevents normal `import`, so we use importlib with
    an explicit file path. Imported once and cached at module scope.
    """
    script_path = Path(__file__).parent / "generate-newsletter.py"
    spec = importlib.util.spec_from_file_location("generate_newsletter", script_path)
    if spec is None or spec.loader is None:  # pragma: no cover - defensive
        raise RuntimeError(f"Unable to load spec for {script_path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


gn = _load_script_module()


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

SAMPLE_CHANGELOG = """# Changelog

All notable changes to this project will be documented in this file.

## [0.4.0] - 2026-01-17

### Added
- New `gt shiny` command for polishing beads
- Support for multi-rig coordination

### Breaking
- **Removed legacy `gt oldcmd`** - Use `gt newcmd` instead; see migration guide.
- **Changed config format** - YAML instead of TOML; run `gt config migrate`.

## [0.3.0] - 2026-01-10

### Added
- Initial public release

## [0.2.0] - 2026-01-04

### Fixed
- Minor bug in bead routing
"""


def _make_changelog(tmp_path: Path) -> Path:
    """Create a fake repo layout with scripts/ and CHANGELOG.md.

    The script resolves `CHANGELOG.md` as
    `Path(__file__).parent.parent / "CHANGELOG.md"`. __file__ refers to the
    real generate-newsletter.py, so we patch the script's module-level
    `__file__` in each test that needs an isolated changelog.
    """
    scripts_dir = tmp_path / "scripts"
    scripts_dir.mkdir()
    fake_script = scripts_dir / "generate-newsletter.py"
    fake_script.write_text("# placeholder\n")
    (tmp_path / "CHANGELOG.md").write_text(SAMPLE_CHANGELOG)
    return fake_script


# ---------------------------------------------------------------------------
# Tests: simple helpers (pricing, provider detection)
# ---------------------------------------------------------------------------

class DetectProviderTests(unittest.TestCase):
    def test_claude_maps_to_anthropic(self):
        self.assertEqual(gn.detect_ai_provider("claude-opus-4-1-20250805"), "anthropic")
        self.assertEqual(gn.detect_ai_provider("claude-sonnet-4-5-20250929"), "anthropic")

    def test_gpt_maps_to_openai(self):
        self.assertEqual(gn.detect_ai_provider("gpt-4o"), "openai")
        self.assertEqual(gn.detect_ai_provider("gpt-4-turbo"), "openai")
        self.assertEqual(gn.detect_ai_provider("openai-something"), "openai")

    def test_reasoning_models_map_to_openai(self):
        self.assertEqual(gn.detect_ai_provider("o1-preview"), "openai")
        self.assertEqual(gn.detect_ai_provider("o3-mini"), "openai")

    def test_unknown_defaults_to_anthropic(self):
        self.assertEqual(gn.detect_ai_provider("llama-42"), "anthropic")


class PricingTests(unittest.TestCase):
    def test_opus_pricing(self):
        self.assertEqual(gn.get_model_pricing("claude-opus-4-1-20250805"), (15.0, 45.0))

    def test_sonnet_pricing(self):
        self.assertEqual(gn.get_model_pricing("claude-sonnet-4-5-20250929"), (3.0, 15.0))

    def test_haiku_pricing(self):
        self.assertEqual(gn.get_model_pricing("claude-haiku-4-5"), (0.80, 4.0))

    def test_gpt4o_pricing(self):
        self.assertEqual(gn.get_model_pricing("gpt-4o"), (5.0, 15.0))

    def test_gpt4_turbo_pricing(self):
        self.assertEqual(gn.get_model_pricing("gpt-4-turbo"), (10.0, 30.0))

    def test_gpt35_pricing(self):
        self.assertEqual(gn.get_model_pricing("gpt-3.5-turbo"), (0.50, 1.50))

    def test_unknown_pricing_is_zero(self):
        self.assertEqual(gn.get_model_pricing("mystery-model"), (0.0, 0.0))


class CostCalculationTests(unittest.TestCase):
    def test_known_model_cost(self):
        # 1M input + 1M output with opus = 15 + 45 = 60.
        cost = gn.calculate_cost("claude-opus-4-1-20250805", 1_000_000, 1_000_000)
        self.assertAlmostEqual(cost, 60.0)

    def test_partial_tokens(self):
        # gpt-4o: $5/M input, $15/M output.
        # 500k input  -> 0.5 * $5  = $2.50
        # 2M  output  -> 2.0 * $15 = $30.00
        # Total                    = $32.50
        cost = gn.calculate_cost("gpt-4o", 500_000, 2_000_000)
        self.assertAlmostEqual(cost, 32.5)

    def test_unknown_model_is_zero(self):
        self.assertEqual(gn.calculate_cost("mystery-model", 10_000, 10_000), 0.0)


class ModelCostInfoTests(unittest.TestCase):
    def test_unknown_label(self):
        self.assertIn("cost unknown", gn.get_model_cost_info("mystery-model"))

    def test_opus_label_contains_prices(self):
        label = gn.get_model_cost_info("claude-opus-4-1-20250805")
        self.assertIn("$15.0", label)
        self.assertIn("$45.0", label)

    def test_gpt4o_label(self):
        label = gn.get_model_cost_info("gpt-4o")
        self.assertIn("gpt-4o", label)
        self.assertIn("$5.0", label)


# ---------------------------------------------------------------------------
# Tests: git branch check
# ---------------------------------------------------------------------------

class CheckGitBranchTests(unittest.TestCase):
    def test_returns_branch_name(self):
        fake_result = types.SimpleNamespace(stdout="main\n")
        with mock.patch.object(gn.subprocess, "run", return_value=fake_result) as run:
            branch = gn.check_git_branch()
        self.assertEqual(branch, "main")
        run.assert_called_once()

    def test_returns_none_on_git_failure(self):
        with mock.patch.object(
            gn.subprocess,
            "run",
            side_effect=gn.subprocess.CalledProcessError(1, "git"),
        ):
            self.assertIsNone(gn.check_git_branch())


# ---------------------------------------------------------------------------
# Tests: changelog/version helpers
# ---------------------------------------------------------------------------

class ChangelogTests(unittest.TestCase):
    def setUp(self):
        self._tmp = Path(
            os.path.join(os.environ.get("TMPDIR", "/tmp"), f"gn_test_{os.getpid()}_{id(self)}")
        )
        self._tmp.mkdir(parents=True, exist_ok=True)
        self._fake_script = _make_changelog(self._tmp)
        # The script uses Path(__file__).parent.parent for the changelog location,
        # so patch gn.__file__ to point inside our fake tree.
        self._patch = mock.patch.object(gn, "__file__", str(self._fake_script))
        self._patch.start()

    def tearDown(self):
        self._patch.stop()
        for f in self._tmp.rglob("*"):
            if f.is_file():
                f.unlink()
        for d in sorted(self._tmp.rglob("*"), reverse=True):
            if d.is_dir():
                d.rmdir()
        self._tmp.rmdir()

    def test_get_all_versions_parses_changelog(self):
        versions = gn.get_all_versions()
        self.assertEqual(len(versions), 3)
        self.assertEqual(versions[0][0], "0.4.0")
        self.assertEqual(versions[0][1], datetime(2026, 1, 17))
        self.assertEqual(versions[-1][0], "0.2.0")

    def test_get_previous_version_returns_first_entry(self):
        version, date = gn.get_previous_version()
        self.assertEqual(version, "0.4.0")
        self.assertEqual(date, datetime(2026, 1, 17))

    def test_get_version_by_release_accepts_v_prefix(self):
        ver, date = gn.get_version_by_release("v0.3.0")
        self.assertEqual(ver, "0.3.0")
        self.assertEqual(date, datetime(2026, 1, 10))

    def test_get_version_by_release_accepts_plain(self):
        ver, date = gn.get_version_by_release("0.2.0")
        self.assertEqual(ver, "0.2.0")
        self.assertEqual(date, datetime(2026, 1, 4))

    def test_get_version_by_release_missing_raises(self):
        with self.assertRaises(ValueError):
            gn.get_version_by_release("v9.9.9")

    def test_get_changelog_section_extracts_range(self):
        section = gn.get_changelog_section("0.4.0")
        self.assertIn("## [0.4.0]", section)
        self.assertIn("gt shiny", section)
        # Should stop before the next ## header
        self.assertNotIn("## [0.3.0]", section)

    def test_get_changelog_section_missing_version_returns_empty(self):
        self.assertEqual(gn.get_changelog_section("9.9.9"), "")


# ---------------------------------------------------------------------------
# Tests: git commit parsing
# ---------------------------------------------------------------------------

class GetCommitsSinceTests(unittest.TestCase):
    def test_parses_multi_line_output(self):
        git_output = (
            "abc123|feat: add thing|Alice|2026-01-15 10:00:00 -0800\n"
            "def456|fix: something|Bob|2026-01-14 09:00:00 -0800\n"
        )
        fake_result = types.SimpleNamespace(stdout=git_output)
        with mock.patch.object(gn.subprocess, "run", return_value=fake_result) as run:
            commits = gn.get_commits_since(datetime(2026, 1, 1))

        self.assertEqual(len(commits), 2)
        self.assertEqual(commits[0], {
            "hash": "abc123",
            "subject": "feat: add thing",
            "author": "Alice",
            "date": datetime(2026, 1, 15),
        })
        self.assertEqual(commits[1]["hash"], "def456")
        self.assertEqual(commits[1]["author"], "Bob")

        # Verify the git command invocation includes the --since flag with ISO date.
        args, _ = run.call_args
        cmd = args[0]
        self.assertEqual(cmd[0], "git")
        self.assertEqual(cmd[1], "log")
        self.assertEqual(cmd[2], "--since=2026-01-01")

    def test_handles_malformed_date_gracefully(self):
        git_output = "abc123|feat: x|Alice|not-a-date\n"
        fake_result = types.SimpleNamespace(stdout=git_output)
        with mock.patch.object(gn.subprocess, "run", return_value=fake_result):
            commits = gn.get_commits_since(datetime(2026, 1, 1))
        self.assertEqual(len(commits), 1)
        self.assertIsNone(commits[0]["date"])

    def test_handles_empty_output(self):
        fake_result = types.SimpleNamespace(stdout="")
        with mock.patch.object(gn.subprocess, "run", return_value=fake_result):
            self.assertEqual(gn.get_commits_since(datetime(2026, 1, 1)), [])

    def test_handles_missing_author_field(self):
        git_output = "abc123|feat: minimal|\n"
        fake_result = types.SimpleNamespace(stdout=git_output)
        with mock.patch.object(gn.subprocess, "run", return_value=fake_result):
            commits = gn.get_commits_since(datetime(2026, 1, 1))
        self.assertEqual(len(commits), 1)
        self.assertEqual(commits[0]["subject"], "feat: minimal")


# ---------------------------------------------------------------------------
# Tests: breaking-change extraction
# ---------------------------------------------------------------------------

class ExtractBreakingChangesTests(unittest.TestCase):
    def test_extracts_titled_bullets(self):
        section = """## [0.4.0]
### Breaking
- **Removed legacy `gt oldcmd`** - Use `gt newcmd` instead; see migration guide.
- **Changed config format** - YAML instead of TOML; run `gt config migrate`.

### Added
- other stuff
"""
        changes = gn.extract_breaking_changes(section)
        self.assertEqual(len(changes), 2)
        self.assertEqual(changes[0]["title"], "Removed legacy `gt oldcmd`")
        self.assertIn("migration guide", changes[0]["description"])
        self.assertEqual(changes[1]["title"], "Changed config format")

    def test_empty_section_returns_empty_list(self):
        self.assertEqual(gn.extract_breaking_changes(""), [])

    def test_no_breaking_subsection_returns_empty(self):
        section = "## [0.4.0]\n### Added\n- new stuff\n"
        self.assertEqual(gn.extract_breaking_changes(section), [])

    def test_caps_at_five_items(self):
        bullets = "\n".join(
            f"- **Change {i}** - description {i}" for i in range(10)
        )
        section = f"## [0.4.0]\n### Breaking\n{bullets}\n"
        changes = gn.extract_breaking_changes(section)
        self.assertEqual(len(changes), 5)


# ---------------------------------------------------------------------------
# Tests: extract_new_commands (git-diff driven)
# ---------------------------------------------------------------------------

class ExtractNewCommandsTests(unittest.TestCase):
    def test_returns_empty_when_versions_missing(self):
        # subprocess.run returns non-zero for both prefix attempts -> []
        bad_result = types.SimpleNamespace(returncode=1, stdout="")
        with mock.patch.object(gn.subprocess, "run", return_value=bad_result):
            self.assertEqual(gn.extract_new_commands("v0.3.0", "v0.4.0"), [])

    def test_parses_cobra_commands_from_changed_files(self):
        # 1st diff call succeeds. Then the function reads each changed file.
        diff_result = types.SimpleNamespace(returncode=0, stdout="cmd/gt/shiny.go\ncmd/gt/existing_test.go\n")
        go_source = (
            'package gt\n'
            'var shinyCmd = &cobra.Command{\n'
            '    Use:   "shiny",\n'
            '    Short: "Polish things shiny",\n'
            '    RunE:  runShiny,\n'
            '}\n'
        )
        with mock.patch.object(gn.subprocess, "run", return_value=diff_result), \
             mock.patch.object(gn.Path, "read_text", return_value=go_source):
            cmds = gn.extract_new_commands("v0.3.0", "v0.4.0")

        self.assertEqual(len(cmds), 1)
        self.assertEqual(cmds[0]["name"], "shiny")
        self.assertEqual(cmds[0]["short"], "Polish things shiny")
        self.assertEqual(cmds[0]["file"], "cmd/gt/shiny.go")

    def test_skips_test_files(self):
        diff_result = types.SimpleNamespace(returncode=0, stdout="cmd/gt/only_test.go\n")
        with mock.patch.object(gn.subprocess, "run", return_value=diff_result):
            self.assertEqual(gn.extract_new_commands("v0.3.0", "v0.4.0"), [])


# ---------------------------------------------------------------------------
# Tests: prompt building
# ---------------------------------------------------------------------------

class BuildNewsletterPromptTests(unittest.TestCase):
    def _basic_commits(self):
        return [
            {"hash": "abc", "subject": "feat: alpha", "author": "A", "date": datetime(2026, 1, 10)},
            {"hash": "def", "subject": "fix: beta", "author": "B", "date": datetime(2026, 1, 11)},
        ]

    def test_includes_all_components(self):
        prompt = gn.build_newsletter_prompt(
            commits=self._basic_commits(),
            changelog="## [0.4.0]\nsome notes",
            version="0.4.0",
            since_date=datetime(2026, 1, 10),
            until_date=datetime(2026, 1, 17),
            new_commands=[{"name": "shiny", "short": "Polish"}],
            breaking_changes=[{"title": "Big change", "description": "details"}],
        )
        self.assertIn("January 10, 2026", prompt)
        self.assertIn("January 17, 2026", prompt)
        self.assertIn("- feat: alpha", prompt)
        self.assertIn("- fix: beta", prompt)
        self.assertIn("## New Commands & Options", prompt)
        self.assertIn("**shiny**", prompt)
        self.assertIn("## Breaking Changes", prompt)
        self.assertIn("**Big change**", prompt)
        self.assertIn("Version: 0.4.0", prompt)

    def test_version_range_header_preferred(self):
        prompt = gn.build_newsletter_prompt(
            commits=[],
            changelog="",
            version="Newsletter",
            since_date=datetime(2026, 1, 1),
            until_date=datetime(2026, 1, 17),
            from_version="0.3.0",
            to_version="0.4.0",
        )
        self.assertIn("Release Range: v0.3.0 to v0.4.0", prompt)

    def test_until_date_defaults_to_now(self):
        with mock.patch.object(gn, "datetime") as fake_dt:
            fake_dt.now.return_value = datetime(2026, 2, 1)
            # Other attributes must still work - forward them to the real datetime.
            fake_dt.side_effect = lambda *a, **k: datetime(*a, **k)
            fake_dt.strptime = datetime.strptime
            prompt = gn.build_newsletter_prompt(
                commits=[],
                changelog="",
                version="Newsletter",
                since_date=datetime(2026, 1, 1),
            )
        self.assertIn("February 01, 2026", prompt)

    def test_no_new_commands_or_breaking_changes(self):
        prompt = gn.build_newsletter_prompt(
            commits=[],
            changelog="",
            version="Newsletter",
            since_date=datetime(2026, 1, 1),
            until_date=datetime(2026, 1, 2),
        )
        self.assertNotIn("## New Commands & Options", prompt)
        self.assertNotIn("## Breaking Changes", prompt)

    def test_truncates_changelog_to_3000_chars(self):
        # Establish the baseline x-count the template itself contributes
        # when the changelog is empty.
        baseline_xs = gn.build_newsletter_prompt(
            commits=[],
            changelog="",
            version="Newsletter",
            since_date=datetime(2026, 1, 1),
            until_date=datetime(2026, 1, 2),
        ).count("x")

        big_changelog = "x" * 10_000
        prompt = gn.build_newsletter_prompt(
            commits=[],
            changelog=big_changelog,
            version="Newsletter",
            since_date=datetime(2026, 1, 1),
            until_date=datetime(2026, 1, 2),
        )
        # Truncation should drop the contributed x-count to exactly 3000
        # (plus whatever the empty template already had).
        self.assertEqual(prompt.count("x"), baseline_xs + 3000)

    def test_caps_commits_at_fifty(self):
        commits = [
            {"hash": f"h{i}", "subject": f"feat: {i}", "author": "A", "date": None}
            for i in range(60)
        ]
        prompt = gn.build_newsletter_prompt(
            commits=commits,
            changelog="",
            version="Newsletter",
            since_date=datetime(2026, 1, 1),
            until_date=datetime(2026, 1, 2),
        )
        self.assertIn("- feat: 0", prompt)
        self.assertIn("- feat: 49", prompt)
        self.assertNotIn("- feat: 50", prompt)


# ---------------------------------------------------------------------------
# Tests: AI client dispatch (mocked - no network calls)
# ---------------------------------------------------------------------------

class GetAIClientTests(unittest.TestCase):
    def test_anthropic_requires_api_key(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            with self.assertRaises(ValueError) as ctx:
                gn.get_ai_client("anthropic")
            self.assertIn("ANTHROPIC_API_KEY", str(ctx.exception))

    def test_openai_requires_api_key(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            with self.assertRaises(ValueError) as ctx:
                gn.get_ai_client("openai")
            self.assertIn("OPENAI_API_KEY", str(ctx.exception))

    def test_unknown_provider_raises(self):
        with self.assertRaises(ValueError):
            gn.get_ai_client("bogus")

    def test_anthropic_client_constructed_with_key(self):
        with mock.patch.dict(os.environ, {"ANTHROPIC_API_KEY": "sk-test"}):
            with mock.patch.object(gn, "Anthropic") as fake_cls:
                client = gn.get_ai_client("anthropic")
            fake_cls.assert_called_once_with(api_key="sk-test")
            self.assertIs(client, fake_cls.return_value)

    def test_openai_client_constructed_with_key(self):
        with mock.patch.dict(os.environ, {"OPENAI_API_KEY": "sk-test"}):
            with mock.patch.object(gn, "OpenAI") as fake_cls:
                client = gn.get_ai_client("openai")
            fake_cls.assert_called_once_with(api_key="sk-test")
            self.assertIs(client, fake_cls.return_value)


class GenerateWithClaudeTests(unittest.TestCase):
    def test_returns_text_and_token_counts(self):
        fake_response = mock.MagicMock()
        fake_response.content = [mock.MagicMock(text="## Newsletter\nhello")]
        fake_response.usage.input_tokens = 1234
        fake_response.usage.output_tokens = 567

        client = mock.MagicMock()
        client.messages.create.return_value = fake_response

        text, inp, out = gn.generate_with_claude(
            client,
            commits=[],
            changelog="",
            version="Newsletter",
            since_date=datetime(2026, 1, 1),
            until_date=datetime(2026, 1, 2),
        )

        self.assertEqual(text, "## Newsletter\nhello")
        self.assertEqual(inp, 1234)
        self.assertEqual(out, 567)
        # Verify the prompt is passed through as a single user message.
        kwargs = client.messages.create.call_args.kwargs
        self.assertEqual(kwargs["messages"][0]["role"], "user")
        self.assertIn("January 01, 2026", kwargs["messages"][0]["content"])
        self.assertEqual(kwargs["max_tokens"], 4000)


class GenerateWithOpenAITests(unittest.TestCase):
    def test_returns_text_and_token_counts(self):
        fake_response = mock.MagicMock()
        fake_response.choices = [mock.MagicMock()]
        fake_response.choices[0].message.content = "## Newsletter\nhi"
        fake_response.usage.prompt_tokens = 100
        fake_response.usage.completion_tokens = 50

        client = mock.MagicMock()
        client.chat.completions.create.return_value = fake_response

        text, inp, out = gn.generate_with_openai(
            client,
            commits=[],
            changelog="",
            version="Newsletter",
            since_date=datetime(2026, 1, 1),
            until_date=datetime(2026, 1, 2),
        )

        self.assertEqual(text, "## Newsletter\nhi")
        self.assertEqual(inp, 100)
        self.assertEqual(out, 50)
        kwargs = client.chat.completions.create.call_args.kwargs
        self.assertEqual(kwargs["model"], "gpt-4o")
        self.assertEqual(kwargs["messages"][0]["role"], "user")


# ---------------------------------------------------------------------------
# Tests: generate_newsletter() end-to-end with mocked AI clients
# ---------------------------------------------------------------------------

class GenerateNewsletterTests(unittest.TestCase):
    def setUp(self):
        self._tmp = Path(
            os.path.join(os.environ.get("TMPDIR", "/tmp"), f"gn_gen_{os.getpid()}_{id(self)}")
        )
        self._tmp.mkdir(parents=True, exist_ok=True)
        self._fake_script = _make_changelog(self._tmp)
        self._patch_file = mock.patch.object(gn, "__file__", str(self._fake_script))
        self._patch_file.start()

    def tearDown(self):
        self._patch_file.stop()
        for f in self._tmp.rglob("*"):
            if f.is_file():
                f.unlink()
        for d in sorted(self._tmp.rglob("*"), reverse=True):
            if d.is_dir():
                d.rmdir()
        self._tmp.rmdir()

    def test_claude_path_with_explicit_dates(self):
        fake_client = mock.MagicMock()
        fake_commits = [
            {"hash": "abc", "subject": "feat: thing", "author": "A", "date": datetime(2026, 1, 10)},
        ]
        with mock.patch.object(gn, "get_ai_client", return_value=fake_client), \
             mock.patch.object(gn, "get_commits_since", return_value=fake_commits), \
             mock.patch.object(
                 gn, "generate_with_claude",
                 return_value=("CLAUDE OUTPUT", 100, 50),
             ) as claude:
            result = gn.generate_newsletter(
                model="claude-opus-4-1-20250805",
                since_date=datetime(2026, 1, 10),
                until_date=datetime(2026, 1, 17),
                version="Newsletter",
            )

        newsletter, version, start, end, inp, out, cost = result
        self.assertEqual(newsletter, "CLAUDE OUTPUT")
        self.assertEqual(version, "Newsletter")
        self.assertEqual(start, datetime(2026, 1, 10))
        self.assertEqual(end, datetime(2026, 1, 17))
        self.assertEqual(inp, 100)
        self.assertEqual(out, 50)
        # cost = 100 * 15/1M + 50 * 45/1M = 0.0015 + 0.00225 = 0.00375
        self.assertAlmostEqual(cost, 0.00375, places=6)
        claude.assert_called_once()

    def test_openai_path_used_for_gpt_model(self):
        fake_client = mock.MagicMock()
        with mock.patch.object(gn, "get_ai_client", return_value=fake_client), \
             mock.patch.object(gn, "get_commits_since", return_value=[]), \
             mock.patch.object(
                 gn, "generate_with_openai",
                 return_value=("OPENAI OUTPUT", 10, 5),
             ) as openai_gen, \
             mock.patch.object(gn, "generate_with_claude") as claude_gen:
            result = gn.generate_newsletter(
                model="gpt-4o",
                since_date=datetime(2026, 1, 10),
                until_date=datetime(2026, 1, 17),
                version="Newsletter",
            )
        newsletter = result[0]
        self.assertEqual(newsletter, "OPENAI OUTPUT")
        openai_gen.assert_called_once()
        claude_gen.assert_not_called()

    def test_default_model_from_env(self):
        fake_client = mock.MagicMock()
        with mock.patch.dict(os.environ, {"AI_MODEL": "gpt-4o"}), \
             mock.patch.object(gn, "get_ai_client", return_value=fake_client), \
             mock.patch.object(gn, "get_commits_since", return_value=[]), \
             mock.patch.object(gn, "generate_with_openai",
                               return_value=("OUT", 1, 1)) as openai_gen:
            gn.generate_newsletter(
                since_date=datetime(2026, 1, 10),
                until_date=datetime(2026, 1, 17),
                version="Newsletter",
            )
        openai_gen.assert_called_once()

    def test_commit_date_filter_excludes_future_commits(self):
        commits = [
            {"hash": "a", "subject": "in range", "author": "A", "date": datetime(2026, 1, 11)},
            {"hash": "b", "subject": "too late", "author": "A", "date": datetime(2026, 1, 20)},
            {"hash": "c", "subject": "no-date", "author": "A", "date": None},
        ]
        captured = {}

        def capture_claude(client, commits_arg, *args, **kwargs):
            captured["commits"] = commits_arg
            return ("out", 1, 1)

        with mock.patch.object(gn, "get_ai_client", return_value=mock.MagicMock()), \
             mock.patch.object(gn, "get_commits_since", return_value=commits), \
             mock.patch.object(gn, "generate_with_claude", side_effect=capture_claude):
            gn.generate_newsletter(
                model="claude-opus-4-1-20250805",
                since_date=datetime(2026, 1, 10),
                until_date=datetime(2026, 1, 17),
                version="Newsletter",
            )

        subjects = [c["subject"] for c in captured["commits"]]
        self.assertIn("in range", subjects)
        self.assertIn("no-date", subjects)  # None date passes the filter
        self.assertNotIn("too late", subjects)


# ---------------------------------------------------------------------------
# Tests: CLI flag parsing (main command)
#
# The typer stub in this suite does not parse argv; testing `main` through the
# CLI layer is out of scope here. Instead we assert that `generate_newsletter`
# receives the right arguments when invoked from the CLI-style helpers, and
# we test the date-range derivation logic directly.
# ---------------------------------------------------------------------------

class CLIFlagLogicTests(unittest.TestCase):
    """Exercise the date/version-derivation logic used by `main`.

    We call the real `main` function directly with keyword arguments (bypassing
    typer's argument parser, which is stubbed). This tests the post-parse
    branches: --from-release, --to-release, --days, --since (date), --since (relative).
    """

    def setUp(self):
        self._tmp = Path(
            os.path.join(os.environ.get("TMPDIR", "/tmp"), f"gn_cli_{os.getpid()}_{id(self)}")
        )
        self._tmp.mkdir(parents=True, exist_ok=True)
        self._fake_script = _make_changelog(self._tmp)
        self._patch_file = mock.patch.object(gn, "__file__", str(self._fake_script))
        self._patch_file.start()

        self._captured = {}

        def fake_gen(**kwargs):
            self._captured.update(kwargs)
            return ("NEWS", "0.0.0", kwargs.get("since_date"), kwargs.get("until_date"), 1, 1, 0.0)

        self._patch_gen = mock.patch.object(gn, "generate_newsletter", side_effect=fake_gen)
        self._patch_gen.start()

        # Silence output & bypass branch check
        self._patch_echo = mock.patch.object(gn.typer, "echo", lambda *a, **k: None)
        self._patch_echo.start()

        # Pretend we're on main so branch check is a no-op
        self._patch_branch = mock.patch.object(gn, "check_git_branch", return_value="main")
        self._patch_branch.start()

    def tearDown(self):
        self._patch_file.stop()
        self._patch_gen.stop()
        self._patch_echo.stop()
        self._patch_branch.stop()
        for f in self._tmp.rglob("*"):
            if f.is_file():
                f.unlink()
        for d in sorted(self._tmp.rglob("*"), reverse=True):
            if d.is_dir():
                d.rmdir()
        self._tmp.rmdir()

    # --- date-derivation branches ------------------------------------------

    def test_from_release_and_to_release(self):
        # Use dry_run=True so we don't touch the filesystem with output.
        gn.main(
            model=None, output="NEWSLETTER.md", dry_run=True, force=True,
            since=None, days=None,
            from_release="v0.3.0", to_release="v0.4.0",
        )
        self.assertEqual(self._captured["since_date"], datetime(2026, 1, 10))
        self.assertEqual(self._captured["until_date"], datetime(2026, 1, 17))
        self.assertEqual(self._captured["version"], "0.3.0 to 0.4.0")
        self.assertEqual(self._captured["from_version"], "v0.3.0")
        self.assertEqual(self._captured["to_version"], "v0.4.0")

    def test_only_from_release(self):
        gn.main(
            model=None, output="NEWSLETTER.md", dry_run=True, force=True,
            since=None, days=None, from_release="v0.3.0", to_release=None,
        )
        self.assertEqual(self._captured["since_date"], datetime(2026, 1, 10))
        self.assertIn("0.3.0 to present", self._captured["version"])

    def test_only_to_release(self):
        gn.main(
            model=None, output="NEWSLETTER.md", dry_run=True, force=True,
            since=None, days=None, from_release=None, to_release="v0.4.0",
        )
        self.assertEqual(self._captured["since_date"], datetime(2000, 1, 1))
        self.assertEqual(self._captured["until_date"], datetime(2026, 1, 17))
        self.assertIn("up to 0.4.0", self._captured["version"])

    def test_days_flag(self):
        frozen_now = datetime(2026, 2, 1)
        with mock.patch.object(gn, "datetime", wraps=datetime) as fake_dt:
            fake_dt.now.return_value = frozen_now
            gn.main(
                model=None, output="NEWSLETTER.md", dry_run=True, force=True,
                since=None, days=30, from_release=None, to_release=None,
            )
        self.assertEqual(self._captured["until_date"], frozen_now)
        self.assertEqual(self._captured["since_date"], frozen_now - timedelta(days=30))
        self.assertEqual(self._captured["version"], "Last 30 days")

    def test_since_relative(self):
        frozen_now = datetime(2026, 2, 1)
        with mock.patch.object(gn, "datetime", wraps=datetime) as fake_dt:
            fake_dt.now.return_value = frozen_now
            gn.main(
                model=None, output="NEWSLETTER.md", dry_run=True, force=True,
                since="14d", days=None, from_release=None, to_release=None,
            )
        self.assertEqual(self._captured["until_date"], frozen_now)
        self.assertEqual(self._captured["since_date"], frozen_now - timedelta(days=14))
        self.assertEqual(self._captured["version"], "Last 14 days")

    def test_since_absolute_date(self):
        frozen_now = datetime(2026, 2, 1)
        with mock.patch.object(gn, "datetime", wraps=datetime) as fake_dt:
            fake_dt.now.return_value = frozen_now
            # datetime.strptime still needs to work on the wrapped class.
            fake_dt.strptime = datetime.strptime
            gn.main(
                model=None, output="NEWSLETTER.md", dry_run=True, force=True,
                since="2025-12-15", days=None, from_release=None, to_release=None,
            )
        self.assertEqual(self._captured["since_date"], datetime(2025, 12, 15))
        self.assertEqual(self._captured["until_date"], frozen_now)
        self.assertEqual(self._captured["version"], "Since 2025-12-15")

    def test_model_flag_is_forwarded(self):
        gn.main(
            model="gpt-4o", output="NEWSLETTER.md", dry_run=True, force=True,
            since=None, days=7, from_release=None, to_release=None,
        )
        self.assertEqual(self._captured["model"], "gpt-4o")


# ---------------------------------------------------------------------------
# Tests: AUTO_COMMIT branch (no-op in tests = subprocess guarded behind env)
# ---------------------------------------------------------------------------

class AutoCommitBranchTests(unittest.TestCase):
    """The AUTO_COMMIT behaviour in main() should only run git commands when
    the env var is set to "true". In the default test environment it is
    unset, so subprocess.run must never be called for commit/push.
    """

    def setUp(self):
        self._tmp = Path(
            os.path.join(os.environ.get("TMPDIR", "/tmp"), f"gn_ac_{os.getpid()}_{id(self)}")
        )
        self._tmp.mkdir(parents=True, exist_ok=True)
        self._fake_script = _make_changelog(self._tmp)
        self._patch_file = mock.patch.object(gn, "__file__", str(self._fake_script))
        self._patch_file.start()

        # Make generate_newsletter a cheap stub.
        self._patch_gen = mock.patch.object(
            gn,
            "generate_newsletter",
            return_value=("NEWS", "0.4.0", datetime(2026, 1, 10), datetime(2026, 1, 17), 1, 1, 0.0),
        )
        self._patch_gen.start()
        self._patch_echo = mock.patch.object(gn.typer, "echo", lambda *a, **k: None)
        self._patch_echo.start()
        self._patch_branch = mock.patch.object(gn, "check_git_branch", return_value="main")
        self._patch_branch.start()

        # Run in the temp dir so NEWSLETTER.md gets written there, not repo root.
        self._orig_cwd = os.getcwd()
        os.chdir(self._tmp)

    def tearDown(self):
        os.chdir(self._orig_cwd)
        self._patch_file.stop()
        self._patch_gen.stop()
        self._patch_echo.stop()
        self._patch_branch.stop()
        for f in self._tmp.rglob("*"):
            if f.is_file():
                f.unlink()
        for d in sorted(self._tmp.rglob("*"), reverse=True):
            if d.is_dir():
                d.rmdir()
        self._tmp.rmdir()

    def test_auto_commit_unset_does_not_run_git(self):
        env = {k: v for k, v in os.environ.items() if k != "AUTO_COMMIT"}
        with mock.patch.dict(os.environ, env, clear=True), \
             mock.patch.object(gn.subprocess, "run") as run:
            gn.main(
                model=None, output="NEWSLETTER.md", dry_run=False, force=True,
                since=None, days=7, from_release=None, to_release=None,
            )
        # Newsletter file was written
        self.assertTrue((self._tmp / "NEWSLETTER.md").exists())
        self.assertEqual((self._tmp / "NEWSLETTER.md").read_text(), "NEWS")
        run.assert_not_called()

    def test_auto_commit_false_does_not_run_git(self):
        with mock.patch.dict(os.environ, {"AUTO_COMMIT": "false"}), \
             mock.patch.object(gn.subprocess, "run") as run:
            gn.main(
                model=None, output="NEWSLETTER.md", dry_run=False, force=True,
                since=None, days=7, from_release=None, to_release=None,
            )
        run.assert_not_called()

    def test_auto_commit_true_invokes_git_add_commit_push(self):
        with mock.patch.dict(os.environ, {"AUTO_COMMIT": "true"}), \
             mock.patch.object(gn.subprocess, "run") as run:
            gn.main(
                model=None, output="NEWSLETTER.md", dry_run=False, force=True,
                since=None, days=7, from_release=None, to_release=None,
            )

        # Expect exactly three subprocess.run calls: add, commit, push.
        self.assertEqual(run.call_count, 3)
        first_args = [call.args[0] for call in run.call_args_list]
        self.assertEqual(first_args[0][:2], ["git", "add"])
        self.assertEqual(first_args[1][:2], ["git", "commit"])
        self.assertEqual(first_args[2], ["git", "push"])

    def test_dry_run_skips_output_file_and_git(self):
        with mock.patch.dict(os.environ, {"AUTO_COMMIT": "true"}), \
             mock.patch.object(gn.subprocess, "run") as run:
            gn.main(
                model=None, output="NEWSLETTER.md", dry_run=True, force=True,
                since=None, days=7, from_release=None, to_release=None,
            )
        # No file written in dry-run mode, no git commands.
        self.assertFalse((self._tmp / "NEWSLETTER.md").exists())
        run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
