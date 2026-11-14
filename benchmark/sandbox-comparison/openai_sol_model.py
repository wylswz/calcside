"""Fixed provider configuration for the supplementary Sol experiment."""

import os

from langchain_openai import ChatOpenAI


MODEL = "gpt-6.1-sol"
BASE_URL = "https://api.openai.com/v1"
MAX_OUTPUT_TOKENS = 16384


def make_model(**unused_previous_provider_settings):
    """Replace the old factory without modifying frozen experimental sources."""
    return ChatOpenAI(
        model=MODEL, base_url=BASE_URL, api_key=os.environ["OPENAI_API_KEY"],
        reasoning_effort="medium", max_tokens=MAX_OUTPUT_TOKENS,
        timeout=180, max_retries=0, use_responses_api=True,
        model_kwargs={"parallel_tool_calls": False},
    )
