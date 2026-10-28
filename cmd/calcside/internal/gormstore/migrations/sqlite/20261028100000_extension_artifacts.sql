CREATE TABLE extension_artifacts (
  source text NOT NULL,
  sum text NOT NULL,
  files text NOT NULL,
  created_at datetime NOT NULL,
  PRIMARY KEY (source, sum)
);
