ALTER TABLE llm_providers ADD COLUMN models text NULL;
UPDATE llm_providers SET models = CASE
  WHEN config IS NOT NULL AND config <> '' AND (config::jsonb ->> 'model') IS NOT NULL AND (config::jsonb ->> 'model') <> ''
    THEN jsonb_build_array(config::jsonb ->> 'model')::text
  WHEN type = 'anthropic' THEN '["claude-haiku-4-5"]'
  WHEN type = 'gemini' THEN '["gemini-2.5-flash"]'
  ELSE '["gpt-5-mini"]'
END;
UPDATE llm_providers SET config = (config::jsonb - 'model')::text
  WHERE config IS NOT NULL AND config <> '';
