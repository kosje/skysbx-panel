-- Users are the billing unit. `name` is what the node reports traffic against,
-- so it is also the identity carried in every inbound's user list.
CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    name          TEXT    NOT NULL UNIQUE,
    vless_uuid    TEXT    NOT NULL,
    password      TEXT    NOT NULL,              -- AnyTLS / Trojan, plain
    ss_password   TEXT    NOT NULL,              -- SS2022 user PSK, base64 of 32 bytes
    sub_token     TEXT    NOT NULL UNIQUE,       -- the opaque part of the subscription URL
    enabled       INTEGER NOT NULL DEFAULT 1,
    expires_at    INTEGER,                       -- unix seconds; NULL = never
    traffic_limit INTEGER NOT NULL DEFAULT 0,    -- bytes; 0 = unlimited
    traffic_used  INTEGER NOT NULL DEFAULT 0,
    note          TEXT    NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL
);

CREATE TABLE nodes (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL UNIQUE,
    token_hash   TEXT    NOT NULL,               -- bcrypt of the join token; the token itself is shown once
    address      TEXT    NOT NULL,               -- what clients connect to (domain or IP)
    country      TEXT    NOT NULL DEFAULT 'XX',
    enabled      INTEGER NOT NULL DEFAULT 1,
    last_seen_at INTEGER,
    version      TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL
);

-- An inbound belongs to exactly one node. That is deliberate: a subscription
-- entry is generated per (user, inbound), so an inbound shared between nodes
-- could only ever be advertised under one address.
CREATE TABLE inbounds (
    id       INTEGER PRIMARY KEY,
    node_id  INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    tag      TEXT    NOT NULL UNIQUE,            -- sing-box inbound tag
    protocol TEXT    NOT NULL,                   -- vless | anytls | shadowsocks
    port     INTEGER NOT NULL,
    config   TEXT    NOT NULL,                   -- sing-box inbound JSON, users left empty
    client   TEXT    NOT NULL,                   -- client-side params for subscriptions, JSON
    enabled  INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX idx_inbounds_node ON inbounds(node_id);

-- No rows for a user means "every inbound", which is the common case for a
-- single-tenant panel. Rows restrict the user to the listed inbounds.
CREATE TABLE user_inbounds (
    user_id    INTEGER NOT NULL REFERENCES users(id)    ON DELETE CASCADE,
    inbound_id INTEGER NOT NULL REFERENCES inbounds(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, inbound_id)
);

-- Daily rollup. Nodes report deltas, so a node restart (which zeroes its own
-- counters) can never make a total go backwards.
CREATE TABLE traffic (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    node_id INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    day     INTEGER NOT NULL,                    -- unix days since epoch
    up      INTEGER NOT NULL DEFAULT 0,
    down    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, node_id, day)
);

CREATE INDEX idx_traffic_day ON traffic(day);

CREATE TABLE settings (
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL
);
