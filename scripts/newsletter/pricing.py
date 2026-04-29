"""Model pricing and cost calculation helpers for the newsletter generator.

Prices are expressed in US dollars per 1 million tokens, separated into
(input_price, output_price). Unknown models return (0.0, 0.0).
"""

from __future__ import annotations


# (input $/1M, output $/1M, display_name)
# Ordering matters: more specific matches come first (e.g. 'opus-4-1' before 'opus').
_PRICING_TABLE: list[tuple[str, float, float, str]] = [
    # Anthropic
    ('opus-4-1',   15.0, 45.0, 'claude-opus-4-1'),
    ('opus-4.1',   15.0, 45.0, 'claude-opus-4-1'),
    ('opus',       15.0, 45.0, 'claude-opus'),
    ('sonnet-4-5', 3.0,  15.0, 'claude-sonnet-4-5'),
    ('sonnet',     3.0,  15.0, 'claude-sonnet'),
    ('haiku-4-5',  0.80, 4.0,  'claude-haiku-4-5'),
    ('haiku',      0.80, 4.0,  'claude-haiku'),
    # OpenAI
    ('gpt-4o',      5.0,  15.0, 'gpt-4o'),
    ('gpt-4-turbo', 10.0, 30.0, 'gpt-4-turbo'),
    ('gpt-4',       30.0, 60.0, 'gpt-4'),
    ('gpt-3.5',     0.50, 1.50, 'gpt-3.5-turbo'),
]


def _match_pricing(model: str) -> tuple[float, float, str] | None:
    """Return (input_price, output_price, display_name) for the first matching row, or None."""
    model_lower = model.lower()
    for key, in_price, out_price, display in _PRICING_TABLE:
        if key in model_lower:
            return in_price, out_price, display
    return None


def get_model_pricing(model: str) -> tuple[float, float]:
    """Get pricing for a model as (input_cost, output_cost) per 1M tokens.

    Unknown models return (0.0, 0.0).
    """
    match = _match_pricing(model)
    if match is None:
        return (0.0, 0.0)
    in_price, out_price, _ = match
    return in_price, out_price


def get_model_cost_info(model: str) -> str:
    """Human-readable cost info string for a model."""
    match = _match_pricing(model)
    if match is None:
        return f"{model} (cost unknown)"
    in_price, out_price, display = match
    return f"{display} (${in_price}/${out_price} per 1M input/output tokens)"


def calculate_cost(model: str, input_tokens: int, output_tokens: int) -> float:
    """Calculate the actual cost of a generation in US dollars."""
    input_price, output_price = get_model_pricing(model)
    input_cost = (input_tokens / 1_000_000) * input_price
    output_cost = (output_tokens / 1_000_000) * output_price
    return input_cost + output_cost


def detect_ai_provider(model: str) -> str:
    """Detect AI provider ('anthropic' or 'openai') from model name.

    Defaults to 'anthropic' for unknown models.
    """
    model_lower = model.lower()
    if 'claude' in model_lower:
        return 'anthropic'
    if 'gpt' in model_lower or 'openai' in model_lower:
        return 'openai'
    if 'o1' in model_lower or 'o3' in model_lower:
        return 'openai'  # OpenAI reasoning models
    return 'anthropic'
