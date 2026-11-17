-- Phase 0: identity, access control, settings, audit, license and the job queue.
-- Times are Unix seconds. Money is an integer in the smallest unit (toman).

CREATE TABLE users (
  id               INTEGER PRIMARY KEY,
  email            TEXT UNIQUE COLLATE NOCASE,
  phone            TEXT,
  name             TEXT NOT NULL DEFAULT '',
  password_hash    TEXT NOT NULL DEFAULT '',
  telegram_chat_id INTEGER UNIQUE,
  telegram_username TEXT,
  is_admin         INTEGER NOT NULL DEFAULT 0,   -- may open /admin (permissions come from roles)
  is_owner         INTEGER NOT NULL DEFAULT 0,   -- full, unrestricted access
  group_id         INTEGER,
  status           TEXT NOT NULL DEFAULT 'active', -- active | blocked
  referral_code    TEXT UNIQUE,
  referred_by      INTEGER REFERENCES users(id),
  totp_secret_enc  TEXT,
  credit_limit     INTEGER NOT NULL DEFAULT 0,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL
);

CREATE TABLE roles (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  permissions TEXT NOT NULL DEFAULT '[]',  -- JSON array of permission ids
  created_at  INTEGER NOT NULL
);

CREATE TABLE user_roles (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role_id INTEGER NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  PRIMARY KEY (user_id, role_id)
);

-- Sessions store only the SHA-256 of the cookie token.
CREATE TABLE sessions (
  token_hash TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  ip         TEXT,
  user_agent TEXT,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL
);
CREATE INDEX idx_sessions_user ON sessions(user_id);

CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,  -- JSON
  updated_at INTEGER NOT NULL
);

CREATE TABLE audit_logs (
  id          INTEGER PRIMARY KEY,
  actor_id    INTEGER,
  action      TEXT NOT NULL,
  target_type TEXT,
  target_id   TEXT,
  before_json TEXT,
  after_json  TEXT,
  ip          TEXT,
  created_at  INTEGER NOT NULL
);
CREATE INDEX idx_audit_created ON audit_logs(created_at);
CREATE INDEX idx_audit_target ON audit_logs(target_type, target_id);

-- Single row: the activated key and the last signed token (both encrypted).
CREATE TABLE license_state (
  id          INTEGER PRIMARY KEY CHECK (id = 1),
  key_enc     TEXT NOT NULL DEFAULT '',
  payload_enc TEXT NOT NULL DEFAULT '',
  sig         TEXT NOT NULL DEFAULT '',
  seen_time   INTEGER NOT NULL DEFAULT 0,  -- monotonic high-water mark against clock rollback
  last_ok     INTEGER NOT NULL DEFAULT 0,
  last_error  TEXT NOT NULL DEFAULT '',
  last_try    INTEGER NOT NULL DEFAULT 0
);
INSERT INTO license_state(id) VALUES (1);

CREATE TABLE jobs (
  id           INTEGER PRIMARY KEY,
  kind         TEXT NOT NULL,
  payload      TEXT NOT NULL DEFAULT '{}',
  dedupe_key   TEXT UNIQUE,           -- optional: at most one pending job per key
  run_at       INTEGER NOT NULL,
  attempts     INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 8,
  locked_until INTEGER NOT NULL DEFAULT 0,
  status       TEXT NOT NULL DEFAULT 'pending', -- pending | running | done | failed
  last_error   TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
CREATE INDEX idx_jobs_due ON jobs(status, run_at);
