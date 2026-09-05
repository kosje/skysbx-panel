-- How each account is being used, by hour.
--
-- Shape, not destinations. What separates BitTorrent, a bulk download and a
-- running speed test from ordinary browsing is how many connections are open at
-- once, to how many different peers, across how many ports. Recording where
-- someone goes would answer a question nobody asked and build a browsing
-- history that did not exist before.
--
-- Peaks rather than averages: an account that opens two hundred connections for
-- ten minutes an hour averages to nothing, and the ten minutes are the point.
CREATE TABLE user_activity (
    user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    node_id  INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    hour     INTEGER NOT NULL,             -- unix hours since epoch
    conns    INTEGER NOT NULL DEFAULT 0,   -- most connections seen in one sample
    peers    INTEGER NOT NULL DEFAULT 0,   -- most distinct destinations in one sample
    ports    INTEGER NOT NULL DEFAULT 0,   -- most distinct destination ports
    ips      INTEGER NOT NULL DEFAULT 0,   -- most distinct source addresses
    samples  INTEGER NOT NULL DEFAULT 0,   -- how many reports went into this row
    PRIMARY KEY (user_id, node_id, hour)
);

CREATE INDEX user_activity_hour ON user_activity(hour);
