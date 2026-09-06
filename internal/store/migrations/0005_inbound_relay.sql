-- Relaying one node's inbound through another node, in the panel.
--
-- The relay node runs a `direct` inbound with override_address/override_port
-- pointing at the origin node's listener: bytes are copied, nothing is decrypted
-- and no credential is involved. That is the whole reason to prefer it over a
-- chained proxy — the origin node still terminates the protocol, so it still
-- counts traffic per user, still enforces address limits and still records
-- activity. A chained proxy would attribute every relayed byte to one internal
-- account and quietly destroy all three.
--
-- relay_node_id is nullable rather than 0-defaulted because a REFERENCES clause
-- added by ALTER TABLE is only legal with a NULL default. NULL means "clients
-- reach this inbound directly", which is what every existing row gets.
--
-- No ON DELETE clause on purpose. Removing a node that others relay through
-- would otherwise silently drop those inbounds back to their own address — the
-- one thing the operator set the relay up to avoid. The service refuses the
-- delete and says which inbounds are in the way; this is the backstop.
ALTER TABLE inbounds ADD COLUMN relay_node_id INTEGER REFERENCES nodes(id);

-- The port the relay node listens on. Usually 443: putting an inbound on the
-- one port every network lets through is most of why a relay exists.
ALTER TABLE inbounds ADD COLUMN relay_port INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_inbounds_relay ON inbounds(relay_node_id);
