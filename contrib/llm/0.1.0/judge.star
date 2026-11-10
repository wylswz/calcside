# Discriminative (judge-style) ops shared by LLM providers. Each function
# takes the provider's `structured` callable so one fixed contract —
# schema in, typed result out — backs every judge op across vendors.

_SCORE_SCHEMA = {
    "type": "object",
    "properties": {
        "score": {"type": "number"},
        "rationale": {"type": "string"},
    },
    "required": ["score", "rationale"],
    "additionalProperties": False,
}

_CHOOSE_SCHEMA = {
    "type": "object",
    "properties": {
        "index": {"type": "integer"},
        "rationale": {"type": "string"},
    },
    "required": ["index", "rationale"],
    "additionalProperties": False,
}

_RANK_SCHEMA = {
    "type": "object",
    "properties": {
        "order": {"type": "array", "items": {"type": "integer"}},
        "rationale": {"type": "string"},
    },
    "required": ["order", "rationale"],
    "additionalProperties": False,
}


def score_impl(structured_fn, provider, content, criteria, scale=1.0, model=None):
    if type(content) != "string" or type(criteria) != "string":
        fail("llm.score: content and criteria must be strings")
    if scale <= 0:
        fail("llm.score: scale must be positive")
    d = structured_fn(provider, [
        {"role": "system", "content":
            "You are a strict evaluator. Score the content against the " +
            "criteria on a scale from 0 to " + str(scale) + "."},
        {"role": "user", "content":
            "Criteria: " + criteria + "\n\nContent:\n" + content},
    ], _SCORE_SCHEMA, model)
    s = d["score"]
    if s < 0 or s > scale:
        fail("llm.score: provider returned out-of-range score " + str(s))
    return {"score": s, "rationale": d["rationale"]}


def choose_impl(structured_fn, provider, instruction, options, model=None):
    if type(options) != "list" or len(options) == 0:
        fail("llm.choose: options must be a non-empty list")
    if len(options) > 64:
        fail("llm.choose: at most 64 options")
    listing = "\n".join([str(i) + ". " + str(o) for i, o in enumerate(options)])
    d = structured_fn(provider, [
        {"role": "system", "content":
            "Pick the single best option index (0-based) for the instruction."},
        {"role": "user", "content": instruction + "\n\nOptions:\n" + listing},
    ], _CHOOSE_SCHEMA, model)
    i = d["index"]
    if type(i) != "int" or i < 0 or i >= len(options):
        fail("llm.choose: provider returned out-of-range index " + str(i))
    return {"index": i, "option": options[i], "rationale": d["rationale"]}


def rank_impl(structured_fn, provider, instruction, items, model=None):
    if type(items) != "list" or len(items) == 0:
        fail("llm.rank: items must be a non-empty list")
    if len(items) > 64:
        fail("llm.rank: at most 64 items")
    listing = "\n".join([str(i) + ". " + str(o) for i, o in enumerate(items)])
    d = structured_fn(provider, [
        {"role": "system", "content":
            "Rank all items for the instruction. Return every 0-based index " +
            "exactly once, best first."},
        {"role": "user", "content": instruction + "\n\nItems:\n" + listing},
    ], _RANK_SCHEMA, model)
    order = d["order"]
    if sorted(order) != list(range(len(items))):
        fail("llm.rank: provider order is not a permutation of the items")
    return {"order": order, "rationale": d["rationale"]}
