-- An indexed lookup key for a node's join token.
--
-- Authentication used to be a linear scan: bcrypt every enabled node's hash
-- against the presented token until one matched. That is the only thing you can
-- do with bcrypt alone, and it made an unauthenticated endpoint cost one bcrypt
-- per node per request — a measured 1.04 seconds of CPU on a twenty-node panel,
-- for a token consisting of the word "wrong". Anyone who could reach the panel
-- could stop it.
--
-- bcrypt is the wrong primitive here in the first place. It exists to make
-- guessing a *low-entropy* secret expensive. A node token is 32 bytes from
-- crypto/rand; there is nothing to guess, and the deliberate slowness only ever
-- taxes the defender. A SHA-256 of the token is both an exact index and a
-- perfectly sound way to store a secret of that size.
--
-- NULL means a node created before this migration, whose token was only ever
-- stored as a bcrypt hash and so cannot be backfilled — the plaintext is gone.
-- Those rows keep working through the old scan, and fill this in the first time
-- they authenticate. See AuthenticateNode.
ALTER TABLE nodes ADD COLUMN token_sha TEXT;

CREATE UNIQUE INDEX idx_nodes_token_sha ON nodes(token_sha) WHERE token_sha IS NOT NULL;
