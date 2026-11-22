# backend/services/llm_client.py
#
# Unified LLM client -- one place that knows how to call Groq, OpenAI,
# Anthropic, or Gemini, so the rest of the codebase asks for "a chat
# completion" without caring which provider is behind it. Swapping
# providers becomes an env var change, not a code change, and a future
# provider deprecation (like llama-3.3-70b-versatile's on 2026-08-16)
# only needs a default-model update in ONE place.
#
# Provider selection:
#   LLM_PROVIDER  -- "groq" (default), "openai", "anthropic", "gemini"
#   LLM_MODEL     -- overrides the provider's default model below
#   Each provider's own API key env var (GROQ_API, OPENAI_API_KEY,
#   ANTHROPIC_API_KEY, GOOGLE_API_KEY) must be set for whichever
#   provider is actually selected -- the others don't need to be.
#
# Only the selected provider's SDK needs to be installed -- every import
# below is lazy (inside complete(), not at module load time), so e.g. an
# all-Groq deployment never needs to `pip install openai anthropic
# google-generativeai` at all.
#
# Pricing snapshot as of 2026-09-07 (verify against each provider's live
# pricing page before relying on this for cost decisions -- rates move
# fast in this space):
#   groq/gpt-oss-120b        $0.15 / $0.60  per 1M input/output tokens
#   openai/gpt-5.6-terra     $2.00 / $12.00
#   anthropic/claude-sonnet-5 $3.00 / $15.00 (post-2026-09-01 rate)
#   gemini/gemini-3.1-pro    $2.00 / $12.00 (up to 200K context)
#
# The table above is now also live data (PRICING_USD_PER_1M below), not
# just a comment -- every call through complete() records its actual
# dollar cost (bondqi_llm_cost_usd_total in monitoring/metrics.py) using
# the token counts each provider's own response actually reports, not an
# estimate. This exists so "is our pricing still profitable if we switch
# to a better model" is a Grafana query, not a guess: per-field plans
# charge $0.208-$0.50 (see hitl-go/routers/payment.go's plan table);
# see COUPON_AND_ALERTING.md's economics note for why that leaves a wide
# margin over any model in this table, including the priciest.
import os

DEFAULT_MODELS = {
    "groq":      "openai/gpt-oss-120b",
    "openai":    "gpt-5.6-terra",
    "anthropic": "claude-sonnet-5",
    "gemini":    "gemini-3.1-pro",
}

PROVIDER_ORDER = ["groq", "openai", "anthropic", "gemini"]

# (input $ / 1M tokens, output $ / 1M tokens). Keyed by (provider, model)
# so LLM_MODEL overrides that pick an unlisted model just record nothing
# rather than silently costing against the wrong price -- see
# record_llm_cost below.
PRICING_USD_PER_1M = {
    ("groq", "openai/gpt-oss-120b"):     (0.15, 0.60),
    ("openai", "gpt-5.6-terra"):         (2.00, 12.00),
    ("anthropic", "claude-sonnet-5"):    (3.00, 15.00),
    ("gemini", "gemini-3.1-pro"):        (2.00, 12.00),
}


def _provider(name: str = None) -> str:
    return (name or os.getenv("LLM_PROVIDER", "groq")).lower()


def _model(provider: str, override: str = None) -> str:
    return override or os.getenv("LLM_MODEL") or DEFAULT_MODELS.get(provider, "")


def record_llm_cost(provider: str, model: str, input_tokens: int, output_tokens: int) -> None:
    """Records a real (not estimated) dollar cost for one completion call,
    using the actual input/output token counts the provider's own
    response reported.

    Unrecognized (provider, model) pairs -- e.g. an LLM_MODEL override
    pointing at something not in PRICING_USD_PER_1M -- record nothing
    rather than guessing at a price, so the cost metric stays trustworthy
    rather than silently wrong. Update PRICING_USD_PER_1M when you add a
    model here.

    Never raises: a metrics failure must not break extraction.
    """
    try:
        prices = PRICING_USD_PER_1M.get((provider, model))
        if prices is None:
            return
        in_price, out_price = prices
        cost = (input_tokens * in_price + output_tokens * out_price) / 1_000_000
        from monitoring.metrics import llm_cost_usd, llm_tokens_total
        llm_cost_usd.labels(model=model).inc(cost)
        llm_tokens_total.labels(model=model, kind="prompt").inc(input_tokens)
        llm_tokens_total.labels(model=model, kind="completion").inc(output_tokens)
    except Exception:
        # Same reasoning as every other metrics call site in this
        # codebase: observability must never be why an extraction fails.
        pass


def complete(prompt: str, max_tokens: int = 500, temperature: float = 0.1,
             provider: str = None, model: str = None) -> str:
    """
    Single-turn chat completion. Returns the raw text response.

    provider/model let ONE call explicitly request a specific provider,
    overriding LLM_PROVIDER/LLM_MODEL just for that call -- used by
    reextractor.py's "different_model" retry strategy, which needs to
    genuinely escalate to a different model after two failed attempts
    on the default one, not just relabel the same call.
    """
    p = _provider(provider)
    m = _model(p, model)

    if p == "groq":
        from groq import Groq
        client = Groq(api_key=os.environ.get("GROQ_API"))
        resp = client.chat.completions.create(
            messages=[{"role": "user", "content": prompt}],
            model=m, max_tokens=max_tokens, temperature=temperature,
        )
        if resp.usage:
            record_llm_cost(p, m, resp.usage.prompt_tokens, resp.usage.completion_tokens)
        return resp.choices[0].message.content

    if p == "openai":
        from openai import OpenAI
        client = OpenAI(api_key=os.environ.get("OPENAI_API_KEY"))
        resp = client.chat.completions.create(
            messages=[{"role": "user", "content": prompt}],
            model=m, max_tokens=max_tokens, temperature=temperature,
        )
        if resp.usage:
            record_llm_cost(p, m, resp.usage.prompt_tokens, resp.usage.completion_tokens)
        return resp.choices[0].message.content

    if p == "anthropic":
        from anthropic import Anthropic
        client = Anthropic(api_key=os.environ.get("ANTHROPIC_API_KEY"))
        resp = client.messages.create(
            model=m, max_tokens=max_tokens, temperature=temperature,
            messages=[{"role": "user", "content": prompt}],
        )
        if resp.usage:
            # Anthropic names these input_tokens/output_tokens, not
            # prompt_tokens/completion_tokens like the OpenAI-shaped
            # clients above.
            record_llm_cost(p, m, resp.usage.input_tokens, resp.usage.output_tokens)
        return resp.content[0].text

    if p == "gemini":
        import google.generativeai as genai
        genai.configure(api_key=os.environ.get("GOOGLE_API_KEY"))
        client = genai.GenerativeModel(m)
        resp = client.generate_content(
            prompt,
            generation_config={"max_output_tokens": max_tokens, "temperature": temperature},
        )
        if getattr(resp, "usage_metadata", None):
            # Gemini's SDK uses its own field names again --
            # prompt_token_count/candidates_token_count.
            record_llm_cost(
                p, m,
                resp.usage_metadata.prompt_token_count,
                resp.usage_metadata.candidates_token_count,
            )
        return resp.text

    raise ValueError(f"Unknown LLM_PROVIDER: {p!r} (want one of {PROVIDER_ORDER})")


def current_provider() -> str:
    """What LLM_PROVIDER actually resolves to right now -- for logging/
    timing metadata, so it never goes stale the way the old hardcoded
    'model': 'llama-3.3-70b-versatile' timing label did."""
    return _provider()


def current_model() -> str:
    return _model(current_provider())


def next_provider(current: str) -> str:
    """
    Given a provider that already failed, return a genuinely different
    one to try next -- a short fixed fallback chain (not a random
    choice) so retry behavior stays predictable and testable. Wraps
    around PROVIDER_ORDER.
    """
    try:
        idx = PROVIDER_ORDER.index((current or "").lower())
    except ValueError:
        idx = -1
    return PROVIDER_ORDER[(idx + 1) % len(PROVIDER_ORDER)]
