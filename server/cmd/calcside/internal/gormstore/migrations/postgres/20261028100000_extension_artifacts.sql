CREATE TABLE extension_artifacts (
  source text NOT NULL,
  sum text NOT NULL,
  files text NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (source, sum)
);
