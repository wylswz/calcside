# LLM capability extension: consumes every workspace provider extension.
# Provider and model are call parameters so scripts can route different
# tasks to different models/providers. The protocol exposes list_models(),
# respond() and structured(); model catalogues live in provider metadata.

load("judge.star", "score_impl", "choose_impl", "rank_impl")


def _provider(name):
    if type(name) != "string" or name not in provider_registry:
        fail("llm: unknown provider " + str(name))
    return provider_registry[name]


def providers():
    return sorted(provider_registry.keys())


def models(provider):
    return _provider(provider).list_models()


def _model(p, model):
    catalogue = p.list_models()
    if model != None:
        m = model
    else:
        m = catalogue[0]
    if m not in catalogue:
        fail("llm: provider does not offer model " + str(m))
    return m


def respond(provider, messages, model=None, options=None):
    p = _provider(provider)
    return p.respond(messages, _model(p, model), options)


def stream(provider, messages, model=None, options=None):
    p = _provider(provider)
    return p.stream(messages, _model(p, model), options)


def structured(provider, messages, schema, model=None, options=None):
    p = _provider(provider)
    m = _model(p, model)
    # Pass options only when given: providers that predate the options
    # parameter (e.g. anthropic, gemini) still accept 3 arguments.
    if options == None:
        return p.structured(messages, schema, m)
    return p.structured(messages, schema, m, options)


def score(provider, content, criteria, scale=1.0, model=None):
    return score_impl(structured, provider, content, criteria, scale, model)


def choose(provider, instruction, options, model=None):
    return choose_impl(structured, provider, instruction, options, model)


def rank(provider, instruction, items, model=None):
    return rank_impl(structured, provider, instruction, items, model)
