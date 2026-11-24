package pgjobs

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nevindra/bites"
)

// schedule is a periodic job: a kind, its arguments, and either an interval or a cron spec.
type schedule struct {
	name     string
	k        *kind
	args     []byte
	interval time.Duration // zero for a cron schedule, which bites.Cron drives

	last time.Time // the last period this instance looked at; read and written by one goroutine at a time under Client.mu
}

// Every makes a job of the kind with args be enqueued once in every period of interval, across
// every instance of the application, with no leader: the periods are counted from the zero time in
// UTC (an interval of 24 hours is a UTC day, one of an hour starts on the hour), and the instance
// that first sees a period records it in the periodic table, in the transaction that enqueues the
// job, so no other instance enqueues it again. The job's ScheduledAt is the start of its period,
// its UniqueKey is name, "@" and the period's Unix seconds, and it has the kind's queue and
// attempts.
//
// A period is seen at the Service's poll (PollEvery) and by Work, so the job is enqueued within a
// poll of the period's start; a period in which no instance ran (all of them stopped) is enqueued
// when the first one starts, once, and the periods before it are not. A new schedule's current
// period runs at once. For a time of day in a time zone, use Cron.
//
// Every panics, naming the line that called it, when name is not a plain name or is the name of
// another schedule of the Client, interval is under PollEvery, args do not encode as JSON, or the
// Client's Service has started.
func (k *Kind[T]) Every(name string, interval time.Duration, args T) {
	where := callerWhere(2)
	if interval < k.c.opt.PollEvery {
		panic(fmt.Sprintf("pgjobs: Kind(%q).Every(%q): the interval %v is under PollEvery (%v): give at least PollEvery, which is how often the periods are looked at\n\tregistered at %s", k.k.name, name, interval, k.c.opt.PollEvery, where))
	}
	k.c.addSchedule(where, "Every", k.k, name, args, interval, nil)
}

// Cron makes a job of the kind with args be enqueued at each time of spec read in loc, across
// every instance of the application, with no leader: each instance runs a bites.Cron (the five
// fields of cron, the daylight-saving rule and the skipped late runs of ADR 0092) on the app's
// clock, and the instance that first records an occurrence in the periodic table, in the
// transaction that enqueues the job, is the one whose job is stored. The job's ScheduledAt is the
// occurrence, its UniqueKey is name, "@" and the occurrence's Unix seconds, and it has the kind's
// queue and attempts. The schedule starts and stops with the Client's Service.
//
// Cron panics, naming the line that called it, for what bites.Cron refuses (a spec that is not a
// schedule, a nil or the machine's own time zone), when name is not a plain name or is the name of
// another schedule of the Client, args do not encode as JSON, or the Client's Service has started.
func (k *Kind[T]) Cron(name, spec string, loc *time.Location, args T) {
	where := callerWhere(2)
	k.c.addSchedule(where, "Cron", k.k, name, args, 0, func(s *schedule) bites.Service {
		var svc bites.Service
		func() {
			defer func() {
				if v := recover(); v != nil {
					msg, _, _ := strings.Cut(fmt.Sprint(v), "\n\tregistered at ")
					panic(fmt.Sprintf("pgjobs: Kind(%q).Cron(%q): %s\n\tregistered at %s", k.k.name, name, msg, where))
				}
			}()
			svc = bites.Cron("pgjobs-"+name, spec, loc, func(ctx context.Context) error {
				_, err := k.c.claimPeriod(ctx, s, bites.ScheduledAt(ctx))
				return err
			})
		}()
		return svc
	})
}

func (c *Client) addSchedule(where, method string, k *kind, name string, args any, interval time.Duration, cron func(*schedule) bites.Service) {
	fail := func(format string, a ...any) {
		panic(fmt.Sprintf("pgjobs: Kind(%q).%s(%q): %s\n\tregistered at %s", k.name, method, name, fmt.Sprintf(format, a...), where))
	}
	if !plainName.MatchString(name) {
		fail("the name is not a plain name: give letters, digits and \".\", \"_\", \":\", \"-\", starting with a letter or digit, at most 100 bytes, such as \"nightly-report\"")
	}
	b, err := json.Marshal(args)
	if err != nil {
		fail("the arguments do not encode as JSON: %v", err)
	}
	if len(b) > MaxArgs {
		fail("the arguments are %d bytes of JSON, over MaxArgs (%d)", len(b), MaxArgs)
	}
	s := &schedule{name: name, k: k, args: b, interval: interval}
	var svc bites.Service
	if cron != nil {
		svc = cron(s)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		fail("the Client's Service has started: add every schedule before the app serves")
	}
	if _, dup := c.schedules[name]; dup {
		fail("a schedule of this name exists: a name is one schedule; give the second its own")
	}
	c.schedules[name] = s
	if cron != nil {
		c.crons = append(c.crons, svc)
	}
}

// enqueuePeriodic enqueues the job of each interval schedule whose current period this instance has
// not looked at yet.
func (c *Client) enqueuePeriodic(ctx context.Context) error {
	c.mu.Lock()
	var due []*schedule
	now := c.now().UTC()
	for _, s := range c.schedules {
		if s.interval > 0 && now.Truncate(s.interval).After(s.last) {
			due = append(due, s)
		}
	}
	c.mu.Unlock()
	var errs []error
	for _, s := range due {
		period := now.Truncate(s.interval)
		if _, err := c.claimPeriod(ctx, s, period); err != nil {
			errs = append(errs, err)
			continue
		}
		c.mu.Lock()
		if period.After(s.last) {
			s.last = period
		}
		c.mu.Unlock()
	}
	return errors.Join(errs...)
}

// claimPeriod records period as the last of s and enqueues its job, in one transaction, unless an
// instance recorded it (or a later one) first. It reports whether this instance enqueued it.
func (c *Client) claimPeriod(ctx context.Context, s *schedule, period time.Time) (bool, error) {
	tx, err := c.opt.Pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("pgjobs: schedule %s: %w", s.name, err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var won bool
	err = tx.QueryRow(ctx, c.q.period, s.name, period).Scan(&won)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("pgjobs: schedule %s: %w", s.name, err)
	}
	key := s.name + "@" + strconv.FormatInt(period.Unix(), 10)
	if _, err := c.enqueue(ctx, tx, s.k, s.args, EnqueueOptions{RunAt: period, UniqueKey: key}); err != nil {
		return false, fmt.Errorf("pgjobs: schedule %s: %w", s.name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("pgjobs: schedule %s: %w", s.name, err)
	}
	c.log().Debug("pgjobs: periodic job enqueued", "schedule", s.name, "kind", s.k.name, "period", period)
	return true, nil
}
