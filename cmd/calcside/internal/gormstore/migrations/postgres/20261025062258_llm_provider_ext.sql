ALTER TABLE llm_providers ADD COLUMN source text NULL;
ALTER TABLE llm_providers ADD COLUMN sum text NULL;
UPDATE llm_providers SET source = CASE type
  WHEN 'anthropic' THEN 'contrib/llm-anthropic'
  WHEN 'gemini' THEN 'contrib/llm-gemini'
  WHEN 'openai_compatible' THEN 'contrib/llm-openai-compat'
  ELSE 'contrib/llm-openai'
END;
ALTER TABLE llm_providers DROP COLUMN type;
