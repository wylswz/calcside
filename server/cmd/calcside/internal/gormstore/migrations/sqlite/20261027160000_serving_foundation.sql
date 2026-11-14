-- EP-0014 persistent serving foundation: content-addressed releases,
-- desired persistent instances, node registry + boot history, and
-- ephemeral replica leases. All timestamps are UTC.
CREATE TABLE serving_releases (
  id text NOT NULL,
  workspace_id text NOT NULL,
  user_id text NOT NULL,
  spec text NOT NULL,
  policies text NOT NULL,
  program text NOT NULL,
  created_at datetime NOT NULL,
  PRIMARY KEY (id)
);
CREATE UNIQUE INDEX idx_serving_releases_ws_id ON serving_releases (workspace_id, id);

CREATE TABLE persistent_instances (
  id text NOT NULL,
  workspace_id text NOT NULL,
  user_id text NOT NULL,
  release_id text NOT NULL,
  labels text NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  generation bigint NOT NULL DEFAULT 1 CHECK (generation >= 1),
  last_epoch bigint NOT NULL DEFAULT 0 CHECK (last_epoch >= 0),
  active_replica_id text NOT NULL DEFAULT '',
  consecutive_failures bigint NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
  retry_after datetime NULL,
  created_at datetime NOT NULL,
  updated_at datetime NOT NULL,
  PRIMARY KEY (id)
);
CREATE UNIQUE INDEX idx_persistent_instances_ws_id ON persistent_instances (workspace_id, id);
CREATE INDEX idx_persistent_instances_enabled ON persistent_instances (enabled, id);

CREATE TABLE serving_nodes (
  node_id text NOT NULL,
  boot_id text NOT NULL,
  addr text NOT NULL,
  incarnation bigint NOT NULL DEFAULT 0 CHECK (incarnation >= 0),
  capacity integer NOT NULL DEFAULT 0 CHECK (capacity >= 0 AND capacity <= 128),
  heartbeat_until datetime NOT NULL,
  updated_at datetime NOT NULL,
  PRIMARY KEY (node_id)
);
CREATE INDEX idx_serving_nodes_heartbeat ON serving_nodes (heartbeat_until);

CREATE TABLE serving_node_boots (
  node_id text NOT NULL,
  boot_id text NOT NULL,
  incarnation bigint NOT NULL CHECK (incarnation > 0),
  PRIMARY KEY (node_id, boot_id)
);

CREATE TABLE serving_replicas (
  id text NOT NULL,
  workspace_id text NOT NULL,
  instance_id text NOT NULL,
  release_id text NOT NULL,
  node_id text NOT NULL,
  boot_id text NOT NULL,
  node_incarnation bigint NOT NULL CHECK (node_incarnation > 0),
  generation bigint NOT NULL CHECK (generation > 0),
  epoch bigint NOT NULL CHECK (epoch > 0),
  state text NOT NULL CHECK (state IN ('preparing','ready','failed','stopped')),
  lease_until datetime NOT NULL,
  error_code text NOT NULL DEFAULT '',
  created_at datetime NOT NULL,
  updated_at datetime NOT NULL,
  PRIMARY KEY (id)
);
CREATE UNIQUE INDEX idx_serving_replicas_inst_epoch ON serving_replicas (instance_id, epoch);
CREATE INDEX idx_serving_replicas_inst ON serving_replicas (instance_id, lease_until, state);
CREATE INDEX idx_serving_replicas_node ON serving_replicas (node_id, boot_id, node_incarnation, lease_until);
