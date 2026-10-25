ALTER TABLE llm_providers ADD COLUMN type text NULL;
UPDATE llm_providers SET type = CASE
  WHEN source LIKE '%anthropic%' THEN 'anthropic'
  WHEN source LIKE '%gemini%' THEN 'gemini'
  WHEN source LIKE '%compat%' THEN 'openai_compatible'
  ELSE 'openai'
END;
ALTER TABLE llm_providers DROP COLUMN source;
ALTER TABLE llm_providers DROP COLUMN sum;
