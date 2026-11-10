CREATE TABLE extension_versions (
  family text NOT NULL,
  version text NOT NULL,
  sum text NOT NULL,
  artifact_source text NOT NULL,
  created_at datetime NOT NULL,
  PRIMARY KEY (family, version),
  FOREIGN KEY (artifact_source, sum) REFERENCES extension_artifacts (source, sum)
);
