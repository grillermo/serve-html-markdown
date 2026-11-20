-- The "OpenAI" path of expansions talks to sota-llms now, so its flag is named
-- after it. Conditional because a database created from a db/schema.sql dumped
-- after this point already has use_sota_llms.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'expansions' AND column_name = 'use_openai'
  ) THEN
    ALTER TABLE expansions RENAME COLUMN use_openai TO use_sota_llms;
  END IF;
END
$$;
