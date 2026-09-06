-- A monthly traffic allowance rather than a lifetime one.
--
-- Without this an operator selling a monthly plan has to zero every account by
-- hand every month, and an account they forget silently stops working the
-- moment it crosses its limit — the same symptom as an expiry, from a different
-- cause, with nothing on the page to tell them apart.
--
-- reset_day is the day of the month the counter goes back to zero. 0 means
-- never, which is what every existing row gets: this exists to be turned on
-- where it is wanted, not to wipe the usage history of accounts that have been
-- working. A day past the end of a short month clamps to that month's last day,
-- so 31 means "the last day of every month" and February is not skipped.
ALTER TABLE users ADD COLUMN reset_day INTEGER NOT NULL DEFAULT 0;

-- When the counter was last zeroed by the schedule. NULL means never, and for a
-- new user it is set to the creation time so that signing someone up on the
-- 20th does not reset them on the 21st for a month they did not have.
--
-- The comparison is against this rather than against a "next due" timestamp, so
-- a panel that was switched off across a reset day performs it on the next
-- start instead of skipping the month.
ALTER TABLE users ADD COLUMN last_reset_at INTEGER;
