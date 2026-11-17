-- Phase 2: servers (tunnel nodes + 3x-ui panels), orders, services, usage,
-- notifications, Telegram links and tickets.

-- kind=tunnel: an Iran server running neosell agent; kind=xui: a 3x-ui panel.
CREATE TABLE nodes (
  id               INTEGER PRIMARY KEY,
  name             TEXT NOT NULL,
  kind             TEXT NOT NULL,              -- tunnel | xui
  location_id      INTEGER REFERENCES locations(id) ON DELETE SET NULL,
  public_host      TEXT NOT NULL DEFAULT '',   -- IP/domain customers connect to
  enabled          INTEGER NOT NULL DEFAULT 1,
  sale_enabled     INTEGER NOT NULL DEFAULT 1, -- 0 = keep running services, sell no new ones
  config_enc       TEXT NOT NULL DEFAULT '',   -- encrypted JSON (panel credentials, NeoTunnel token …)
  options          TEXT NOT NULL DEFAULT '{}', -- port ranges, reserved ports, cores, capacity …
  agent_token_hash TEXT UNIQUE,
  status           TEXT NOT NULL DEFAULT 'unknown', -- online | offline | error | unknown
  status_detail    TEXT NOT NULL DEFAULT '',
  info             TEXT NOT NULL DEFAULT '{}', -- reported by the agent (version, ips, cores)
  last_seen_at     INTEGER NOT NULL DEFAULT 0,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL
);

-- Admin-editable tunnel core definitions (how to install/run Kariz, Backpack …).
CREATE TABLE core_templates (
  id            INTEGER PRIMARY KEY,
  core          TEXT NOT NULL UNIQUE,       -- neotunnel | kariz | backpack | <custom>
  title         TEXT NOT NULL,
  driver        TEXT NOT NULL,              -- neotunnel | template
  binary_url    TEXT NOT NULL DEFAULT '',   -- {arch} = amd64|arm64
  server_config TEXT NOT NULL DEFAULT '',   -- Go text/template
  server_exec   TEXT NOT NULL DEFAULT '',
  client_config TEXT NOT NULL DEFAULT '',
  client_exec   TEXT NOT NULL DEFAULT '',
  config_ext    TEXT NOT NULL DEFAULT 'toml',
  options       TEXT NOT NULL DEFAULT '{}',
  enabled       INTEGER NOT NULL DEFAULT 1,
  updated_at    INTEGER NOT NULL
);

-- 3x-ui inbounds sold as outbounds.
CREATE TABLE outbound_templates (
  id          INTEGER PRIMARY KEY,
  node_id     INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  inbound_id  INTEGER NOT NULL,
  location_id INTEGER NOT NULL REFERENCES locations(id) ON DELETE CASCADE,
  label       TEXT NOT NULL DEFAULT '',
  public_host TEXT NOT NULL DEFAULT '',
  public_port INTEGER NOT NULL DEFAULT 0,
  tag         TEXT NOT NULL DEFAULT '',
  info        TEXT NOT NULL DEFAULT '{}',  -- cached inbound (protocol, port, streamSettings)
  enabled     INTEGER NOT NULL DEFAULT 1,
  created_at  INTEGER NOT NULL,
  UNIQUE (node_id, inbound_id)
);

CREATE TABLE orders (
  id              INTEGER PRIMARY KEY,
  user_id         INTEGER NOT NULL REFERENCES users(id),
  kind            TEXT NOT NULL,         -- new | renew | addon
  product_id      INTEGER REFERENCES products(id) ON DELETE SET NULL,
  service_id      INTEGER,
  selection       TEXT NOT NULL,
  quote           TEXT NOT NULL,
  amount          INTEGER NOT NULL,
  discount_id     INTEGER,
  status          TEXT NOT NULL DEFAULT 'paid', -- paid | refunded
  idempotency_key TEXT UNIQUE,
  created_at      INTEGER NOT NULL
);
CREATE INDEX idx_orders_user ON orders(user_id, id);

CREATE TABLE services (
  id            INTEGER PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id),
  product_id    INTEGER REFERENCES products(id) ON DELETE SET NULL,
  type          TEXT NOT NULL,           -- port | outbound
  model         TEXT NOT NULL,
  title         TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL,           -- provisioning | waiting_peer | active | limited | expired | paused | suspended | failed | deleted
  spec          TEXT NOT NULL DEFAULT '{}',
  quota_bytes   INTEGER NOT NULL DEFAULT 0, -- 0 = unlimited (port services; outbound uses per-location quotas)
  used_up       INTEGER NOT NULL DEFAULT 0,
  used_down     INTEGER NOT NULL DEFAULT 0,
  period_start  INTEGER NOT NULL DEFAULT 0,
  expires_at    INTEGER NOT NULL DEFAULT 0,  -- 0 = no end
  rate_mbit     INTEGER NOT NULL DEFAULT 0,
  max_ips       INTEGER NOT NULL DEFAULT 0,
  node_id       INTEGER REFERENCES nodes(id) ON DELETE SET NULL,
  core          TEXT NOT NULL DEFAULT '',
  delivery      TEXT NOT NULL DEFAULT 'shared',
  proto         TEXT NOT NULL DEFAULT 'tcp',
  sub_token     TEXT NOT NULL UNIQUE,
  xray_uuid     TEXT NOT NULL DEFAULT '',
  notified      TEXT NOT NULL DEFAULT '{}',  -- which alerts were already sent this period
  status_reason TEXT NOT NULL DEFAULT '',
  status_at     INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);
CREATE INDEX idx_services_user ON services(user_id, id);
CREATE INDEX idx_services_status ON services(status, expires_at);

CREATE TABLE service_ports (
  id         INTEGER PRIMARY KEY,
  service_id INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  node_id    INTEGER NOT NULL REFERENCES nodes(id),
  port       INTEGER NOT NULL,
  to_port    INTEGER NOT NULL DEFAULT 0,
  class_id   INTEGER NOT NULL DEFAULT 0,
  active     INTEGER NOT NULL DEFAULT 1
);
-- A port is sold once per node (released when the service is deleted).
CREATE UNIQUE INDEX idx_service_ports_unique ON service_ports(node_id, port) WHERE active = 1;

-- Dedicated tunnels (customer's own foreign server) and shared tunnels.
CREATE TABLE tunnels (
  id          INTEGER PRIMARY KEY,
  node_id     INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  service_id  INTEGER REFERENCES services(id) ON DELETE CASCADE, -- NULL for shared tunnels
  core        TEXT NOT NULL,
  name        TEXT NOT NULL,              -- name on the node (e.g. ns12 or NeoTunnel t1)
  mode        TEXT NOT NULL DEFAULT 'shared',
  bridge_port INTEGER NOT NULL DEFAULT 0, -- port the far end connects to
  secret      TEXT NOT NULL DEFAULT '',   -- token shared with the far end
  peer_token  TEXT UNIQUE,                -- install-script token (/i/<token>)
  peer_status TEXT NOT NULL DEFAULT '',   -- waiting | connected
  peer_setup  TEXT NOT NULL DEFAULT '',   -- e.g. NeoTunnel apply command
  params      TEXT NOT NULL DEFAULT '{}',
  created_at  INTEGER NOT NULL,
  UNIQUE (node_id, name)
);

CREATE TABLE service_locations (
  id          INTEGER PRIMARY KEY,
  service_id  INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  location_id INTEGER NOT NULL REFERENCES locations(id),
  sub_id      TEXT NOT NULL UNIQUE,
  quota_bytes INTEGER NOT NULL DEFAULT 0,
  used_up     INTEGER NOT NULL DEFAULT 0,
  used_down   INTEGER NOT NULL DEFAULT 0,
  expires_at  INTEGER NOT NULL DEFAULT 0,
  status      TEXT NOT NULL DEFAULT 'provisioning',
  UNIQUE (service_id, location_id)
);

CREATE TABLE service_location_clients (
  id                  INTEGER PRIMARY KEY,
  service_location_id INTEGER NOT NULL REFERENCES service_locations(id) ON DELETE CASCADE,
  template_id         INTEGER NOT NULL REFERENCES outbound_templates(id),
  email               TEXT NOT NULL UNIQUE,
  uuid                TEXT NOT NULL,
  created             INTEGER NOT NULL DEFAULT 0, -- 1 once present on the panel
  last_up             INTEGER NOT NULL DEFAULT 0,  -- last absolute counters seen on the panel
  last_down           INTEGER NOT NULL DEFAULT 0
);

-- Absolute counters last reported per (source, service) to turn totals into deltas.
CREATE TABLE usage_counters (
  source     TEXT NOT NULL,   -- "node:<id>"
  service_id INTEGER NOT NULL,
  up         INTEGER NOT NULL,
  down       INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (source, service_id)
);

CREATE TABLE usage_hourly (
  service_id INTEGER NOT NULL,
  hour       INTEGER NOT NULL,  -- unix time truncated to the hour
  up         INTEGER NOT NULL DEFAULT 0,
  down       INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (service_id, hour)
);

CREATE TABLE notifications (
  id         INTEGER PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,
  title      TEXT NOT NULL,
  body       TEXT NOT NULL DEFAULT '',
  read_at    INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX idx_notifications_user ON notifications(user_id, id);

CREATE TABLE telegram_links (
  token      TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL
);

CREATE TABLE tickets (
  id         INTEGER PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  subject    TEXT NOT NULL,
  status     TEXT NOT NULL DEFAULT 'open', -- open | answered | closed
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE ticket_messages (
  id         INTEGER PRIMARY KEY,
  ticket_id  INTEGER NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  user_id    INTEGER NOT NULL,
  is_staff   INTEGER NOT NULL DEFAULT 0,
  body       TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

-- Default core definitions. Kariz/Backpack templates are filled by the admin
-- (config format per their docs); NeoTunnel is driven through its local API.
INSERT INTO core_templates(core, title, driver, updated_at) VALUES
  ('neotunnel', 'NeoTunnel', 'neotunnel', strftime('%s','now')),
  ('kariz', 'Kariz', 'template', strftime('%s','now')),
  ('backpack', 'Backpack', 'template', strftime('%s','now'));
