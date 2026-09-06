package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kosje/skysbx-panel/internal/store"
)

func at(y int, m time.Month, d, h int) time.Time {
	return time.Date(y, m, d, h, 0, 0, 0, time.Local)
}

// A day past the end of a short month has to land on that month's last day.
// Skipping February would mean an account on the 31st got a two-month
// allowance every year, and one on the 30th got it every February.
func TestResetDayClampsToTheEndOfShortMonths(t *testing.T) {
	for _, tc := range []struct {
		name string
		day  int
		now  time.Time
		want time.Time
	}{
		{"mid-month, already passed", 5, at(2026, time.March, 20, 12), at(2026, time.March, 5, 0)},
		{"mid-month, not yet", 25, at(2026, time.March, 20, 12), at(2026, time.February, 25, 0)},
		{"31st in a 31-day month", 31, at(2026, time.March, 31, 12), at(2026, time.March, 31, 0)},
		// February 2026 has 28 days, so the 31st is the 28th.
		{"31st in February", 31, at(2026, time.February, 28, 12), at(2026, time.February, 28, 0)},
		{"30th in February", 30, at(2026, time.February, 28, 12), at(2026, time.February, 28, 0)},
		// 2028 is a leap year: the 29th exists.
		{"29th in a leap February", 29, at(2028, time.February, 29, 12), at(2028, time.February, 29, 0)},
		// Stepping back across a year boundary.
		{"January, not yet", 20, at(2026, time.January, 10, 12), at(2025, time.December, 20, 0)},
	} {
		if got := LastScheduledReset(tc.day, tc.now); !got.Equal(tc.want) {
			t.Errorf("%s: LastScheduledReset(%d, %s) = %s, want %s",
				tc.name, tc.day, tc.now.Format("2006-01-02"),
				got.Format("2006-01-02"), tc.want.Format("2006-01-02"))
		}
	}
}

func TestNextScheduledResetIsStrictlyAhead(t *testing.T) {
	for _, tc := range []struct {
		day  int
		now  time.Time
		want time.Time
	}{
		{5, at(2026, time.March, 20, 12), at(2026, time.April, 5, 0)},
		{25, at(2026, time.March, 20, 12), at(2026, time.March, 25, 0)},
		// On the day itself, at noon: the reset instant is midnight, so it has
		// already happened and the next one is next month.
		{20, at(2026, time.March, 20, 12), at(2026, time.April, 20, 0)},
		{31, at(2026, time.January, 31, 12), at(2026, time.February, 28, 0)},
		{15, at(2026, time.December, 20, 12), at(2027, time.January, 15, 0)},
	} {
		if got := NextScheduledReset(tc.day, tc.now); !got.Equal(tc.want) {
			t.Errorf("NextScheduledReset(%d, %s) = %s, want %s", tc.day,
				tc.now.Format("2006-01-02"), got.Format("2006-01-02"),
				tc.want.Format("2006-01-02"))
		}
	}
}

func TestClampResetDay(t *testing.T) {
	for in, want := range map[int]int{-5: 0, 0: 0, 1: 1, 28: 28, 31: 31, 32: 0, 999: 0} {
		if got := ClampResetDay(in); got != want {
			t.Errorf("ClampResetDay(%d) = %d, want %d", in, got, want)
		}
	}
}

func resetFixture(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st)
}

// The whole point: a monthly allowance goes back to zero on its day without
// anyone touching it, and crossing back under a limit has to reach the nodes —
// access is revoked by omission from the push, so it is restored the same way.
func TestDueResetZeroesTheCounterAndTellsTheNodes(t *testing.T) {
	svc := resetFixture(t)
	spy := &spyNotifier{}

	u, err := svc.CreateUser(NewUser{Name: "alice", TrafficLimit: 1 << 30, ResetDay: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Over the limit, so inactive.
	node, _, _ := svc.CreateNode("tokyo", "jp.example.com", "JP")
	if _, err := svc.Store().DB().Exec(
		`UPDATE users SET traffic_used = ?, last_reset_at = ? WHERE id = ?`,
		int64(2<<30), at(2026, time.January, 1, 0).Unix(), u.ID); err != nil {
		t.Fatal(err)
	}
	_ = node
	if got, _ := svc.User(u.ID); got.Active(time.Now()) {
		t.Fatal("fixture: the user should be over their limit")
	}

	svc.SetNotifier(spy)
	n, err := svc.RunDueResets()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reset %d users, want 1", n)
	}
	got, _ := svc.User(u.ID)
	if got.TrafficUsed != 0 {
		t.Errorf("traffic_used = %d after a reset", got.TrafficUsed)
	}
	if !got.Active(time.Now()) {
		t.Error("the user is still inactive after their allowance was restored")
	}
	if spy.users == 0 {
		t.Error("the nodes were not told; the account stays cut off until an unrelated edit")
	}

	// Running again immediately must do nothing — the stamp says this cycle is
	// already done.
	spy.users = 0
	if n, err := svc.RunDueResets(); err != nil || n != 0 {
		t.Errorf("a second run reset %d users (err %v); it should be idempotent", n, err)
	}
	if spy.users != 0 {
		t.Error("a no-op run still pushed to the nodes")
	}
}

// Signing someone up shortly before their reset day must not wipe their
// allowance the next morning for a month they never had.
func TestANewUserIsNotResetImmediately(t *testing.T) {
	svc := resetFixture(t)
	// Whatever today is, a reset day of yesterday means the most recent
	// scheduled reset is in the past — but it is before this account existed.
	yesterday := time.Now().AddDate(0, 0, -1).Day()
	u, err := svc.CreateUser(NewUser{Name: "alice", ResetDay: yesterday})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store().DB().Exec(
		`UPDATE users SET traffic_used = 12345 WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}

	if n, err := svc.RunDueResets(); err != nil || n != 0 {
		t.Fatalf("a brand new user was reset straight away (%d, %v)", n, err)
	}
	if got, _ := svc.User(u.ID); got.TrafficUsed != 12345 {
		t.Errorf("traffic was cleared: %d", got.TrafficUsed)
	}
}

// A panel that was switched off across somebody's reset day has to catch up on
// the next start rather than losing the month. That is why the comparison is
// against the last reset and not against a stored "next due".
func TestAMissedResetIsCaughtUpOnTheNextRun(t *testing.T) {
	svc := resetFixture(t)
	u, err := svc.CreateUser(NewUser{Name: "alice", ResetDay: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Last reset two months ago; the panel was down since.
	long := time.Now().AddDate(0, -2, 0)
	if _, err := svc.Store().DB().Exec(
		`UPDATE users SET traffic_used = 999, last_reset_at = ? WHERE id = ?`,
		long.Unix(), u.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.RunDueResets(); n != 1 {
		t.Fatalf("a missed reset was not caught up: %d", n)
	}
	if got, _ := svc.User(u.ID); got.TrafficUsed != 0 {
		t.Errorf("traffic_used = %d", got.TrafficUsed)
	}
}

// Zero means never, which is what every account has until someone asks for a
// schedule. Turning this on must not touch anyone who did not opt in.
func TestUsersWithoutAScheduleAreLeftAlone(t *testing.T) {
	svc := resetFixture(t)
	u, err := svc.CreateUser(NewUser{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store().DB().Exec(
		`UPDATE users SET traffic_used = 777, last_reset_at = NULL WHERE id = ?`,
		u.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.RunDueResets(); n != 0 {
		t.Fatalf("reset %d users with no schedule", n)
	}
	if got, _ := svc.User(u.ID); got.TrafficUsed != 777 {
		t.Errorf("traffic was cleared for a user with no schedule: %d", got.TrafficUsed)
	}
}

// A manual reset stamps the schedule too, or the hourly sweep would zero the
// account again an hour after the operator already did it by hand.
func TestAManualResetSatisfiesTheSchedule(t *testing.T) {
	svc := resetFixture(t)
	u, err := svc.CreateUser(NewUser{Name: "alice", ResetDay: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store().DB().Exec(
		`UPDATE users SET last_reset_at = ? WHERE id = ?`,
		time.Now().AddDate(0, -2, 0).Unix(), u.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetUserTraffic(u.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.RunDueResets(); n != 0 {
		t.Errorf("the scheduler reset an account that was just reset by hand")
	}
}

// Switching a schedule on must not retroactively wipe a counter.
//
// This is the case every account is in right after the feature is added, so
// getting it wrong empties the whole panel once. An account that has been
// running for months would otherwise be measured against a cycle boundary in
// the past, be found overdue, and lose its usage on the next sweep.
func TestTurningTheScheduleOnDoesNotWipeAnything(t *testing.T) {
	svc := resetFixture(t)
	u, err := svc.CreateUser(NewUser{Name: "alice", TrafficLimit: 50 << 30})
	if err != nil {
		t.Fatal(err)
	}
	// An old account: created months ago, plenty of usage, never reset.
	if _, err := svc.Store().DB().Exec(
		`UPDATE users SET traffic_used = ?, created_at = ?, last_reset_at = NULL
		 WHERE id = ?`, int64(30<<30),
		time.Now().AddDate(0, -8, 0).Unix(), u.ID); err != nil {
		t.Fatal(err)
	}

	// The operator turns on a reset day that already passed this month.
	got, _ := svc.User(u.ID)
	got.ResetDay = ClampResetDay(time.Now().AddDate(0, 0, -1).Day())
	if got.ResetDay == 0 {
		t.Skip("first of the month; no earlier day to test with")
	}
	if err := svc.UpdateUser(got); err != nil {
		t.Fatal(err)
	}

	if n, err := svc.RunDueResets(); err != nil || n != 0 {
		t.Fatalf("turning the schedule on reset %d accounts (%v)", n, err)
	}
	after, _ := svc.User(u.ID)
	if after.TrafficUsed != 30<<30 {
		t.Errorf("usage was wiped: %d", after.TrafficUsed)
	}
	if after.LastResetAt == nil {
		t.Error("the cycle was left without a start point; it would wipe later")
	}
}

// A row that predates the column has a schedule but no start point only if one
// was written directly. The sweep must give it a start point rather than treat
// "never reset" as "overdue".
func TestAScheduleWithNoStartPointIsStampedNotReset(t *testing.T) {
	svc := resetFixture(t)
	u, err := svc.CreateUser(NewUser{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store().DB().Exec(
		`UPDATE users SET traffic_used = 4242, reset_day = 1, last_reset_at = NULL
		 WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}

	if n, _ := svc.RunDueResets(); n != 0 {
		t.Error("a schedule with no start point was treated as overdue")
	}
	after, _ := svc.User(u.ID)
	if after.TrafficUsed != 4242 {
		t.Errorf("usage was wiped: %d", after.TrafficUsed)
	}
	if after.LastResetAt == nil {
		t.Fatal("no start point was recorded, so the next sweep would wipe it")
	}

	// And from here the schedule works normally.
	if _, err := svc.Store().DB().Exec(
		`UPDATE users SET last_reset_at = ? WHERE id = ?`,
		time.Now().AddDate(0, -2, 0).Unix(), u.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.RunDueResets(); n != 1 {
		t.Error("the schedule does not fire once it has a start point")
	}
}
