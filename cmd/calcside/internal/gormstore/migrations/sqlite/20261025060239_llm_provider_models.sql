ALTER TABLE llm_providers ADD COLUMN models text NULL;
UPDATE llm_providers SET models = CASE
  WHEN json_valid(config) AND json_extract(config, '$.model') IS NOT NULL AND json_extract(config, '$.model') <> ''
    THEN json_array(json_extract(config, '$.model'))
  WHEN type = 'anthropic' THEN '["claude-haiku-4-5"]'
  WHEN type = 'gemini' THEN '["gemini-2.5-flash"]'
  ELSE '["gpt-5-mini"]'
END;
UPDATE llm_providers SET config = json_remove(config, '$.model')
  WHERE config IS NOT NULL AND json_valid(config);
