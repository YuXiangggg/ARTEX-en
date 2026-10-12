package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// This file handles claiming delivery tasks and their state transitions.
//
// Claiming uses a "lease" rather than a long-running transaction: the row is set
// to sending and next_attempt_at is pushed into the future as the lease expiry,
// then the transaction commits before the network delivery happens. This way no
// database locks are held during delivery - a network request can take several
// seconds (15s client timeout), and holding row locks that long would drag down
// other writes on the same database.
//
// The cost is that if the process crashes mid-delivery, the row is left in
// sending. This is **self-healing**: once the lease expires, next_attempt_at is
// in the past and the next claim round picks the same row up again (see
// state IN ('pending','sending') in the claim conditions). The attempt counter
// is incremented at claim time, so a crash cannot cause infinite retries - once
// MaxNotifyAttempts chances are used up the row lands in failed for manual handling.

// MaxNotifyAttempts is the maximum number of attempts for a delivery (including the first).
// Defined here rather than in the delivery engine: it is the state machine's own
// policy; the engine merely executes it.
const MaxNotifyAttempts = 3

// MaxDigestBatchSize is the maximum number of deliveries merged into a single digest batch.
//
// The reason it exists is resources: if a digest period turns up tens of thousands
// of findings (entirely possible - one full scan can do it), an unbounded claim
// would read every row into memory and render one enormous message, most of which
// would then be cut off by the channel's length limit - wasting memory and
// **silently dropping** the truncated findings. With a cap, the overflow stays in
// the database as the next batch and goes out naturally in the next period; nothing is lost.
//
// Why 500: it is roughly the amount that still leaves "readable content" after
// rendering within WeCom's 4096-byte limit; anything larger just moves the
// truncation point further back.
const MaxDigestBatchSize = 500

// NotificationDelivery is a delivery task, including the channel config and event snapshot needed for rendering.
type NotificationDelivery struct {
	ID            int64           `json:"id"`
	EventID       int64           `json:"event_id"`
	ChannelID     int64           `json:"channel_id"`
	State         string          `json:"state"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt time.Time       `json:"next_attempt_at"`
	LastError     string          `json:"last_error"`
	BatchID       *int64          `json:"batch_id,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	SentAt        *time.Time      `json:"sent_at,omitempty"`
	Snapshot      json.RawMessage `json:"snapshot,omitempty"`
	// Rendering context loaded via join; not serialized to JSON (the server layer assembles the DTO).
	Channel *NotificationChannel `json:"-"`
	// FindingID/EventKind are carried over from the event so the history list can link straight to the finding detail.
	FindingID int64  `json:"finding_id,string"`
	EventKind string `json:"event_kind"`
	// ChannelName/ChannelKind are redundant fields for list display, saving the frontend a second query.
	ChannelName string `json:"channel_name"`
	ChannelKind string `json:"channel_kind"`
}

const notificationDeliveryCols = `d.id, d.event_id, d.channel_id, d.state, d.attempts, d.next_attempt_at,
       d.last_error, d.batch_id, d.created_at, d.sent_at`

// joinedDeliveryQuery is the unified read shape for delivery rows: delivery + event snapshot + channel config.
// Rendering a message needs all three; querying them separately would cost three round trips.
const joinedDeliveryQuery = `SELECT ` + notificationDeliveryCols + `,
       e.snapshot, e.kind, e.finding_id,
       c.id, c.name, c.kind, c.enabled, c.config, c.mode, c.filter, c.rate_per_min
FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id
JOIN notification_channels c ON c.id = d.channel_id`

func scanNotificationDelivery(sc interface{ Scan(...any) error }) (*NotificationDelivery, error) {
	var (
		dl        NotificationDelivery
		lastErr   sql.NullString
		batchID   sql.NullInt64
		sentAt    sql.NullTime
		snapshot  []byte
		eventKind string
		channel   NotificationChannel
		chEnabled bool
	)
	if err := sc.Scan(&dl.ID, &dl.EventID, &dl.ChannelID, &dl.State, &dl.Attempts, &dl.NextAttemptAt,
		&lastErr, &batchID, &dl.CreatedAt, &sentAt,
		&snapshot, &eventKind, &dl.FindingID,
		&channel.ID, &channel.Name, &channel.Kind, &chEnabled, &channel.Config, &channel.Mode, &channel.Filter, &channel.RatePerMin); err != nil {
		return nil, err
	}
	dl.LastError = lastErr.String
	if batchID.Valid {
		dl.BatchID = &batchID.Int64
	}
	if sentAt.Valid {
		dl.SentAt = &sentAt.Time
	}
	dl.Snapshot = json.RawMessage(snapshot)
	dl.EventKind = eventKind
	dl.ChannelName = channel.Name
	dl.ChannelKind = channel.Kind
	channel.Enabled = &chEnabled
	dl.Channel = &channel
	return &dl, nil
}

// claimQuery describes one claim: first select and lock candidates via sel, then
// set them to sending and extend the lease. The caller marks the lease position in
// sel with a $n placeholder and passes the argument itself.
type claimQuery struct {
	sql  string
	args []any
}

// ClaimRealtimeDeliveries claims a batch of due realtime deliveries for a channel, at most limit rows.
//
// It deliberately claims per **single channel** rather than "claim a global batch
// and then pick what to send": the rate limiter in the delivery engine is kept per
// channel, and only by first knowing how many messages this channel may still send
// this round, then claiming exactly that many rows, does rate limiting avoid
// consuming retry attempts. If we claimed first and discarded later, rows held back
// by the rate limiter would already have had attempts incremented once, the
// 3-attempt budget would be burned by pure waiting, and they would end up in failed.
//
// The conditions include "sending with an expired lease" - that is where crash
// self-healing lands. lease must be significantly larger than the worst-case time
// of a single delivery (15s channel HTTP client timeout), otherwise the same row
// could be delivered by two dispatchers at once. Disabled channels are also
// filtered out: disabling already marks existing deliveries as skipped, and this
// is a second guard against rows slipping through when disabling races with claiming.
func (d *DB) ClaimRealtimeDeliveries(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	return d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now()
  AND c.enabled AND c.mode = $4
ORDER BY dd.next_attempt_at, dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $5`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, NotifyModeRealtime, limit},
	}, nil)
}

// DigestBatchDue reports whether the channel has accumulated a due batch: there are
// pending deliveries, and the age of **the oldest one** has reached the digest period.
//
// The decision is based on the oldest delivery's age rather than the wall clock:
// that way a freshly created channel won't immediately emit a one-item "digest"
// just because it aligned to the top of the hour, and a long-backlogged batch
// won't wait another round for nothing.
//
// It is separate from ClaimDigestBatch because the semantics differ: this function
// only answers "should we send", while claiming takes **all** pending rows of the
// channel (including those not yet old enough) - otherwise one period would be
// split into multiple messages and the digest would lose its point.
func (d *DB) DigestBatchDue(ctx context.Context, channelID int64, minAge time.Duration) (bool, error) {
	var due bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM notification_deliveries d
  JOIN notification_channels c ON c.id = d.channel_id
  WHERE d.channel_id = $1 AND d.state IN ($2,$3) AND c.enabled
  GROUP BY d.channel_id
  HAVING min(d.created_at) <= now() - make_interval(secs => $4)
)`, channelID, NotifyStatePending, NotifyStateSending, int64(minAge.Seconds())).Scan(&due)
	return due, err
}

// ClaimDigestBatch claims the channel's currently due pending deliveries as one
// digest batch, at most MaxDigestBatchSize per batch.
//
// All deliveries in a batch share a batch_id, using the smallest id in the set as
// the batch number (stable, readable, no extra sequence needed). On retry, COALESCE
// keeps the original batch number so that "these N were sent together" still holds
// across multiple retries.
//
// The first N are taken in ascending id order rather than at random: the earliest
// deliveries go out first, so a backlog cannot starve old findings behind new ones.
func (d *DB) ClaimDigestBatch(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	// limit is a **memory cap**; callers pass MaxDigestBatchSize. It is clamped
	// again here to guard against a caller passing a larger value.
	//
	// The rate-limit quota is deliberately not accepted as the batch size: rate
	// limiting is measured in messages - one batch sends one message and consumes
	// one token, deducted by takeTokens in the server layer - which is a different
	// dimension from "how many findings fit in a batch". We once passed the per-round
	// request budget in as the batch size to make rate_per_min apply to digests, and
	// a channel with rate=20/min ended up with only 1 finding per batch - digest
	// degraded into realtime pushes with digest wording. To change rate limiting,
	// change the want in takeTokens; do not touch this.
	if limit > MaxDigestBatchSize {
		limit = MaxDigestBatchSize
	}
	out, err := d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now() AND c.enabled
ORDER BY dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $4`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, limit},
	}, func(tx *sql.Tx, ids []int64) error {
		batchID := ids[0]
		for _, id := range ids {
			if id < batchID {
				batchID = id
			}
		}
		ph, idArgs := placeholders(2, ids)
		_, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET batch_id = COALESCE(batch_id, $1)
WHERE id IN (`+ph+`)`, append([]any{batchID}, idArgs...)...)
		return err
	})
	return out, err
}

// claimDeliveries performs "select + set sending and extend lease + load full rows", all in one transaction.
// postClaim is an optional extra step (digest batches use it to write batch_id).
func (d *DB) claimDeliveries(ctx context.Context, lease time.Duration, cq claimQuery, postClaim func(*sql.Tx, []int64) error) ([]*NotificationDelivery, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful commit

	ids, err := selectForClaim(ctx, tx, cq.sql, cq.args...)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, tx.Commit()
	}
	// Set sending and push next_attempt_at into the future: that future moment is the
	// lease expiry, so "lease not yet expired" and "retry time not yet reached" share
	// the same condition, with no extra column needed.
	ph, idArgs := placeholders(3, ids)
	if _, err := tx.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=attempts+1, next_attempt_at=now()+make_interval(secs => $2)
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateSending, lease.Seconds()}, idArgs...)...); err != nil {
		return nil, err
	}
	if postClaim != nil {
		if err := postClaim(tx, ids); err != nil {
			return nil, err
		}
	}
	out, err := loadDeliveriesTx(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func selectForClaim(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func loadDeliveriesTx(ctx context.Context, tx *sql.Tx, ids []int64) ([]*NotificationDelivery, error) {
	ph, args := placeholders(1, ids)
	rows, err := tx.QueryContext(ctx, joinedDeliveryQuery+` WHERE d.id IN (`+ph+`) ORDER BY d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dl)
	}
	return out, rows.Err()
}

// MarkDeliveriesSent marks a batch of deliveries as sent.
func (d *DB) MarkDeliveriesSent(ctx context.Context, ids []int64) error {
	ph, args := placeholders(2, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, sent_at=now(), last_error='' WHERE id IN (`+ph+`)`, append([]any{NotifyStateSent}, args...)...)
	return err
}

// RescheduleDeliveries returns a batch of deliveries to pending and postpones the retry time.
//
// Returning to pending instead of introducing a new intermediate state keeps
// "how many chances remain" expressed in exactly one place (MaxNotifyAttempts),
// so the state machine's branches don't grow with the retry policy.
func (d *DB) RescheduleDeliveries(ctx context.Context, ids []int64, delay time.Duration, errMsg string) error {
	ph, args := placeholders(4, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, next_attempt_at=now()+make_interval(secs => $2), last_error=$3
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, delay.Seconds(), truncateNotifyError(errMsg)}, args...)...)
	return err
}

// DeferDeliveries returns a batch of deliveries to pending, immediately claimable
// again, and **reverts the attempt counted at claim time**.
//
// It has exactly one use: when a digest message is sent in segments due to the
// channel's length limit, items that didn't fit into this message must wait for the
// next batch. That is not a failure, so it should not consume retry budget - attempts
// was optimistically incremented at claim time and must be decremented here.
// Otherwise a 500-item backlog split into 25 segments of 20 would have its tail items
// marked failed by MaxNotifyAttempts by the 3rd segment, despite never having errored.
//
// GREATEST(...,0) covers the case where someone manually retried (resetting attempts
// to zero) and then ended up here, keeping the counter from going negative.
func (d *DB) DeferDeliveries(ctx context.Context, ids []int64, reason string) error {
	ph, args := placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=GREATEST(attempts-1, 0), next_attempt_at=now(), last_error=$2
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, truncateNotifyError(reason)}, args...)...)
	return err
}

// FailDeliveries marks a batch of deliveries as permanently failed, awaiting a manual retry from the delivery history.
func (d *DB) FailDeliveries(ctx context.Context, ids []int64, errMsg string) error {
	// Placeholders start at $3: $1 is state, $2 is last_error.
	ph, args := placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries SET state=$1, last_error=$2 WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateFailed, truncateNotifyError(errMsg)}, args...)...)
	return err
}

// RetryNotificationDelivery manually retries a delivery: reset to pending, zero the
// attempt counter, and make it due immediately. Resetting the counter is deliberate -
// a manual "retry" means the cause of the earlier failures has been addressed, so
// limiting it by the old count makes no sense.
func (d *DB) RetryNotificationDelivery(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$2, attempts=0, next_attempt_at=now(), last_error=''
WHERE id=$1 AND state IN ($3,$4)`, id, NotifyStatePending, NotifyStateFailed, NotifyStateSkipped)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("delivery %d not found or its current state does not allow retry", id)
	}
	return nil
}

// NotificationDeliveryFilter holds the query conditions for delivery history.
type NotificationDeliveryFilter struct {
	ChannelID int64
	State     string
	EventKind string
}

func (f NotificationDeliveryFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.ChannelID > 0 {
		args = append(args, f.ChannelID)
		conds = append(conds, fmt.Sprintf("d.channel_id=$%d", len(args)))
	}
	if f.State != "" {
		args = append(args, f.State)
		conds = append(conds, fmt.Sprintf("d.state=$%d", len(args)))
	}
	if f.EventKind != "" {
		args = append(args, f.EventKind)
		conds = append(conds, fmt.Sprintf("e.kind=$%d", len(args)))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListNotificationDeliveries returns paginated delivery history, newest first.
func (d *DB) ListNotificationDeliveries(ctx context.Context, f NotificationDeliveryFilter, page, pageSize int) ([]*NotificationDelivery, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	where, args := f.where()

	var total int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := fmt.Sprintf("%s%s ORDER BY d.id DESC LIMIT $%d OFFSET $%d",
		joinedDeliveryQuery, where, len(args)+1, len(args)+2)
	rows, err := d.QueryContext(ctx, q, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, dl)
	}
	return out, total, rows.Err()
}

// truncateNotifyError truncates the error message to a length the column accepts. Response
// bodies returned by channels can be long (especially generic webhooks hitting self-hosted
// services), and without truncation the history list payload would bloat.
func truncateNotifyError(msg string) string {
	const max = 500
	if len(msg) <= max {
		return msg
	}
	// Back off to a character boundary to avoid leaving half a UTF-8 character that the frontend would render as garbage.
	cut := max
	for cut > 0 && !isUTF8Start(msg[cut]) {
		cut--
	}
	return msg[:cut] + "…"
}

func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }

// placeholders generates a $n placeholder string starting at start, plus the matching args, for use in IN (...).
// For example start=3, ids=[7,8] -> "$3,$4", [7,8].
func placeholders(start int, ids []int64) (string, []any) {
	ph := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		ph = append(ph, fmt.Sprintf("$%d", start+i))
		args = append(args, id)
	}
	return strings.Join(ph, ","), args
}
