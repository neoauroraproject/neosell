-- Phase 1: wallet, payments, catalog and pricing rules.
-- Money: integer in the shop currency's smallest unit (toman).

CREATE TABLE user_groups (
  id           INTEGER PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  discount_pct REAL NOT NULL DEFAULT 0,   -- applied on top of rule prices
  created_at   INTEGER NOT NULL
);

CREATE TABLE wallets (
  user_id    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  balance    INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);

-- Append-only ledger. balance_after makes every row self-auditing.
CREATE TABLE wallet_transactions (
  id              INTEGER PRIMARY KEY,
  user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  amount          INTEGER NOT NULL,          -- + credit, - debit
  type            TEXT NOT NULL,             -- topup | purchase | refund | admin | referral | payg
  ref_type        TEXT,
  ref_id          TEXT,
  balance_after   INTEGER NOT NULL,
  note            TEXT NOT NULL DEFAULT '',
  created_by      INTEGER,
  idempotency_key TEXT UNIQUE,
  created_at      INTEGER NOT NULL
);
CREATE INDEX idx_wtx_user ON wallet_transactions(user_id, id);

CREATE TABLE bank_cards (
  id      INTEGER PRIMARY KEY,
  number  TEXT NOT NULL,
  holder  TEXT NOT NULL DEFAULT '',
  bank    TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  sort    INTEGER NOT NULL DEFAULT 0,
  used    INTEGER NOT NULL DEFAULT 0    -- for round-robin
);

CREATE TABLE payments (
  id            INTEGER PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  method        TEXT NOT NULL,              -- card | gateway | crypto
  amount        INTEGER NOT NULL,           -- requested
  pay_amount    INTEGER NOT NULL,           -- what the customer must pay (unique for card)
  status        TEXT NOT NULL DEFAULT 'pending', -- pending | submitted | approved | rejected | expired
  card_id       INTEGER REFERENCES bank_cards(id),
  card_snapshot TEXT,                       -- JSON of the card shown to the customer
  receipt_file  TEXT,
  customer_note TEXT NOT NULL DEFAULT '',
  admin_note    TEXT NOT NULL DEFAULT '',
  gateway       TEXT,
  gateway_ref   TEXT,
  reviewed_by   INTEGER,
  reviewed_at   INTEGER,
  expires_at    INTEGER NOT NULL,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);
CREATE INDEX idx_payments_status ON payments(status, id);
CREATE INDEX idx_payments_user ON payments(user_id, id);

CREATE TABLE locations (
  id   INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  flag TEXT NOT NULL DEFAULT '',
  sort INTEGER NOT NULL DEFAULT 0
);

-- A product is something customers can buy; options holds the admin's
-- configuration (sale models, packages, min/max, allowed nodes …) as JSON.
CREATE TABLE products (
  id             INTEGER PRIMARY KEY,
  type           TEXT NOT NULL,               -- port | outbound
  title          TEXT NOT NULL,
  description    TEXT NOT NULL DEFAULT '',
  enabled        INTEGER NOT NULL DEFAULT 1,
  visible_groups TEXT NOT NULL DEFAULT '[]',  -- [] = everyone
  stock          INTEGER NOT NULL DEFAULT -1, -- -1 = unlimited
  sold           INTEGER NOT NULL DEFAULT 0,
  sort           INTEGER NOT NULL DEFAULT 0,
  options        TEXT NOT NULL DEFAULT '{}',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);

CREATE TABLE port_classes (
  id   INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,          -- specific | range | random
  expr TEXT NOT NULL DEFAULT '', -- "443,8443" or "2000-2999"
  sort INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE price_rules (
  id         INTEGER PRIMARY KEY,
  product_id INTEGER REFERENCES products(id) ON DELETE CASCADE, -- NULL = all products
  component  TEXT NOT NULL,
  scope      TEXT NOT NULL DEFAULT '{}',  -- extra match keys (class_id, location_id, core …)
  value      REAL NOT NULL DEFAULT 0,
  tiers      TEXT NOT NULL DEFAULT '[]',
  group_id   INTEGER REFERENCES user_groups(id) ON DELETE CASCADE,
  user_id    INTEGER REFERENCES users(id) ON DELETE CASCADE,
  priority   INTEGER NOT NULL DEFAULT 0,
  valid_from INTEGER NOT NULL DEFAULT 0,
  valid_to   INTEGER NOT NULL DEFAULT 0,
  enabled    INTEGER NOT NULL DEFAULT 1,
  note       TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX idx_rules_product ON price_rules(product_id);

CREATE TABLE discounts (
  id          INTEGER PRIMARY KEY,
  code        TEXT NOT NULL UNIQUE COLLATE NOCASE,
  kind        TEXT NOT NULL,            -- percent | amount
  value       REAL NOT NULL,
  max_uses    INTEGER NOT NULL DEFAULT 0, -- 0 = unlimited
  used        INTEGER NOT NULL DEFAULT 0,
  per_user    INTEGER NOT NULL DEFAULT 1,
  min_amount  INTEGER NOT NULL DEFAULT 0,
  product_ids TEXT NOT NULL DEFAULT '[]', -- [] = all
  first_order_only INTEGER NOT NULL DEFAULT 0,
  valid_from  INTEGER NOT NULL DEFAULT 0,
  valid_to    INTEGER NOT NULL DEFAULT 0,
  enabled     INTEGER NOT NULL DEFAULT 1,
  created_at  INTEGER NOT NULL
);
