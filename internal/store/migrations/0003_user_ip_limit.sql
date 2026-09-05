-- How many distinct source addresses a user may have connected at once.
--
-- A subscription is a file. Nothing stops the person it was issued to from
-- posting it, and without a cap on concurrent addresses one account becomes
-- fifty, on the same bandwidth bill.
--
-- Zero means no limit, which is what every existing row gets: this exists to
-- be turned on where it is wanted, not to surprise an account that has been
-- working.
ALTER TABLE users ADD COLUMN ip_limit INTEGER NOT NULL DEFAULT 0;
