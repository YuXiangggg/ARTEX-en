package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// Global setting keys (stored in the settings key-value table; no dedicated table needed).
const (
	// settingNotifyEnabled is the master push switch. On by default: it is a one-click kill switch for maintenance,
	// not the feature's enabling condition - the real enabling condition is "whether any channel is configured".
	settingNotifyEnabled = "notify_enabled"
	// settingNotifyPublicBaseURL is the external base URL used to build finding detail deep links
	// (e.g. https://artex.example.com). When empty, messages carry no deep link button.
	// The project has no reusable external URL setting, so a new one is added here.
	settingNotifyPublicBaseURL = "notify_public_base_url"
	// settingNotifyDigestMinutes is the digest mode period (minutes).
	settingNotifyDigestMinutes = "notify_digest_interval_min"
)

const (
	// notifyTick is the delivery engine's polling interval. 3 seconds is the upper bound on the engine's latency,
	// and the main source of delay between "finding persisted" and "message arrives in the IM".
	notifyTick = 3 * time.Second
	// notifyLease is the lease duration when claiming deliveries. It must be well above the worst-case time of a single delivery
	// (the notify package's HTTP client timeout is 15 seconds), otherwise the same row could be delivered by two
	// dispatchers at once.
	notifyLease = 3 * time.Minute
	// notifyFanOutPerTick limits the number of events fanned out per tick, so that enabling a channel for the first time
	// doesn't expand the entire historical backlog into delivery tasks at once.
	notifyFanOutPerTick = 200
	// notifyDefaultDigestMinutes is the default digest period.
	notifyDefaultDigestMinutes = 30
	// notifyUnlimitedBurstPerTick is the per-tick delivery cap when a channel has no rate limit.
	// It exists to prevent "a channel configured with no rate limit + one scan producing thousands of findings" from
	// turning a single loop iteration into a long block.
	notifyUnlimitedBurstPerTick = 50
	// notifyMaxSendsPerChannelPerTick is the maximum number of deliveries per channel per tick.
	//
	// This cap is derived from the **lease duration**: claimed rows are given a lease (notifyLease = 3 minutes),
	// and if a tick delivers so many items serially that the worst case exceeds the lease, the lease expires before the last few are sent.
	// Within a single process this doesn't matter (Run is a single goroutine running serially; ticks don't re-enter), but when **two
	// processes share one DB**, the peer will re-claim rows whose lease expired and send them again, and will also
	// double-increment attempts, marking them failed while the original process is still delivering.
	//
	// Value: 3-minute lease / 30-second per-send timeout = 6 would **use up the lease exactly** with zero margin,
	// so it can't be used; 5 gives a worst case of 150 seconds with 30 seconds of margin. This relationship is pinned by
	// TestNotifyTickBudgetFitsWithinLease - changing notifyLease,
	// notifySendTimeout or this value will make that assertion fail.
	notifyMaxSendsPerChannelPerTick = 5
	// notifySendTimeout is the timeout for a single delivery. It also determines the previous constant's value;
	// their product must not exceed notifyLease, see TestNotifyTickBudgetFitsWithinLease.
	notifySendTimeout = 30 * time.Second
)

// notifyBackoff is the backoff sequence for failed retries, indexed by the number of attempts made.
// 3 chances (including the first) correspond to db.MaxNotifyAttempts; the two must be changed together.
var notifyBackoff = []time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
}

// Notifier is the delivery engine for finding push notifications.
//
// It runs as an independent goroutine alongside Scheduler (see server.New). It deliberately does not reuse
// Scheduler's tick: push latency requirements (3 seconds) differ from the triggers' business cadence,
// and failures in the two must not affect each other - a stuck push must not affect agent triggering.
type Notifier struct {
	s  *Server
	pg *db.DB

	// mu protects buckets. There are few channels and low contention, so a single mutex is enough;
	// a finer-grained structure isn't worth it.
	mu      sync.Mutex
	buckets map[int64]*notifyBucket
}

// notifyBucket is a single channel's token bucket.
//
// A token bucket is used instead of a "count per minute then reset" window because the latter has bad boundary effects:
// sending 20 at the end of a window and another 20 the next instant is 40 within a second from the platform's view,
// which gets rate-limited; a token bucket refills at a constant rate and naturally avoids such bursts.
type notifyBucket struct {
	tokens   float64
	lastFill time.Time
}

func newNotifier(s *Server) *Notifier {
	return &Notifier{s: s, pg: s.m.pg, buckets: map[int64]*notifyBucket{}}
}

// Run loops until ctx is done. Started once by server.New.
func (n *Notifier) Run(ctx context.Context) {
	if n.pg == nil {
		return
	}
	t := time.NewTicker(notifyTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.step(ctx)
		}
	}
}

// step runs one round: first fan out new events, then deliver due tasks.
//
// Any failed step is only logged and never breaks the loop - notification system faults must never escalate into process-level problems.
// Each tick is independent; the next round naturally retries.
func (n *Notifier) step(ctx context.Context) {
	if !n.enabled() {
		return
	}
	if _, _, err := n.pg.FanOutPendingEvents(ctx, notifyFanOutPerTick); err != nil {
		log.Printf("[notify] failed to fan out events: %v", err)
		return
	}
	channels, err := n.pg.ListNotificationChannels(ctx)
	if err != nil {
		log.Printf("[notify] failed to read channels: %v", err)
		return
	}
	baseURL := n.publicBaseURL()
	for _, ch := range channels {
		if !ch.IsEnabled() {
			continue
		}
		// The token bucket's unit is **message count** (equivalent to HTTP request count), not finding count.
		// In realtime mode the two are the same (one finding, one message); in digest mode a whole batch of findings is combined
		// into one message, so it consumes only one token.
		//
		// Both modes ask the token bucket first, then claim according to the quota - the order must not be reversed, or deliveries
		// blocked by the rate limit would already have consumed a retry attempt.
		now := time.Now()
		if ch.Mode == db.NotifyModeDigest {
			tokens, claimLimit := digestTickPlan()
			if n.takeTokens(ch.ID, ch.RatePerMin, tokens, now) <= 0 {
				continue
			}
			n.stepDigest(ctx, ch, claimLimit, baseURL)
			continue
		}
		allow := n.takeTokens(ch.ID, ch.RatePerMin, notifyMaxSendsPerChannelPerTick, now)
		if allow <= 0 {
			continue
		}
		n.stepRealtime(ctx, ch, allow, baseURL)
	}
}

// digestTickPlan returns a digest channel's token consumption for this tick and the upper bound on batch size.
//
// The two return values are in **two different dimensions**, which is exactly why this is its own function:
//
//   - tokens is the message count. A batch of findings is combined into one message and sent as one HTTP request, so it is always 1.
//     rate_per_min therefore still applies to digest (at most that many digest messages per minute).
//   - claimLimit is how many findings this batch holds at most. It is only bounded by memory, unrelated to the request budget.
//
// To make rate_per_min apply to digest, the per-tick request budget
// (notifyMaxSendsPerChannelPerTick, derived from the lease) used to be passed down directly as the batch size.
// As a result a rate_per_min=20 channel refilled only 1 token per 3-second tick, so each digest
// message held just 1 finding - digest degraded into "realtime push with digest wording", and readers received a stream of
// "1 new finding in the last 30 minutes", while db.MaxDigestBatchSize was never reachable.
//
// This symptom is hard to spot in end-to-end tests (existing cases all pass stepDigest a large enough limit by hand,
// bypassing the quota calculation in step), so the decision is kept here and pinned directly by
// TestDigestTickPlanDecouplesBatchSizeFromSendBudget.
func digestTickPlan() (tokens, claimLimit int) {
	return 1, db.MaxDigestBatchSize
}

// stepRealtime claims and delivers a channel's realtime tasks, one message per finding.
func (n *Notifier) stepRealtime(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	deliveries, err := n.pg.ClaimRealtimeDeliveries(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] failed to claim realtime deliveries channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("channel type %q is not registered", ch.Kind))
		return
	}
	for _, dl := range deliveries {
		msg, err := n.renderSingle(ctx, dl, baseURL)
		if err != nil {
			// A render failure is a local data problem; retrying won't help.
			_ = n.pg.FailDeliveries(ctx, []int64{dl.ID}, err.Error())
			continue
		}
		n.send(ctx, channel, cfg, msg, []*db.NotificationDelivery{dl})
	}
}

// stepDigest combines a channel's pending deliveries into one message and sends it when the batch is due.
func (n *Notifier) stepDigest(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	window := n.digestInterval()
	due, err := n.pg.DigestBatchDue(ctx, ch.ID, window)
	if err != nil {
		log.Printf("[notify] failed to check digest batch channel=%d: %v", ch.ID, err)
		return
	}
	if !due {
		return
	}
	deliveries, err := n.pg.ClaimDigestBatch(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] failed to claim digest batch channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("channel type %q is not registered", ch.Kind))
		return
	}
	msg, included, err := n.renderBatch(ctx, deliveries, baseURL, int(window.Minutes()))
	if err != nil {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), err.Error())
		return
	}
	// Deliveries whose snapshot is broken and didn't make it into the message must be explicitly failed. Otherwise they stay
	// outside included, in neither the message nor the failure list - on a successful send their state would be
	// missed by the subsequent bulk marking, stuck in sending forever until the lease expires and they're claimed again and again.
	if skipped := excludeDeliveries(deliveries, included); len(skipped) > 0 {
		reason := "event snapshot could not be parsed; this finding cannot be rendered into a message"
		if fErr := n.pg.FailDeliveries(ctx, deliveryIDs(skipped), reason); fErr != nil {
			log.Printf("[notify] failed to mark broken-snapshot deliveries as failed channel=%s ids=%v: %v", ch.Kind, deliveryIDs(skipped), fErr)
		}
		log.Printf("[notify] skipped %d deliveries with unparseable snapshots channel=%d", len(skipped), ch.ID)
	}
	// Only pass the ones that made it into the message to send: included[i] strictly corresponds to msg.Items[i],
	// and send relies on this correspondence to apply "the channel reported fitting the first K items" to the right delivery rows.
	n.send(ctx, channel, cfg, msg, included)
}

// send delivers and transitions state based on the result.
//
// A batch of deliveries (possibly dozens in digest mode) shares one send result: either delivered or the whole batch is retried.
// No per-item retry - the digest is a single message, and resending part of it would break batch semantics.
//
// The only exception is **segmentation caused by the channel length limit**: if the channel reports that only the first K items fit,
// items K+1 onward must be kept for the next batch rather than being marked successful along with the rest. Otherwise the truncated
// findings are neither in the message nor in the failure list, and vanish completely.
func (n *Notifier) send(ctx context.Context, channel notify.Channel, cfg map[string]any, msg notify.Message, deliveries []*db.NotificationDelivery) {
	// Cap a single delivery so one stuck channel can't hold up all the remaining channels in this round.
	sendCtx, cancel := context.WithTimeout(ctx, notifySendTimeout)
	defer cancel()
	delivered, err := channel.Send(sendCtx, cfg, msg)
	if err == nil && delivered > 0 {
		if delivered > len(deliveries) {
			// The reported count can't exceed the number of deliveries; if it does, the render layer miscounted.
			// Treat it as all delivered and log the problem - better than corrupting the records.
			log.Printf("[notify] channel reported delivered count %d exceeding delivery count %d channel=%s, treating as all delivered",
				delivered, len(deliveries), channel.Kind())
			delivered = len(deliveries)
		}
		sent, rest := deliveries[:delivered], deliveries[delivered:]
		if err := n.pg.MarkDeliveriesSent(ctx, deliveryIDs(sent)); err != nil {
			log.Printf("[notify] failed to mark delivered channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(sent), err)
		}
		if len(rest) > 0 {
			// This message hit the channel length limit: requeue the rest immediately for the next tick to continue.
			// Use DeferDeliveries rather than RescheduleDeliveries - this isn't a failure
			// and shouldn't consume the retry budget (claiming already optimistically did +1; it's decremented there).
			if err := n.pg.DeferDeliveries(ctx, deliveryIDs(rest),
				fmt.Sprintf("this message hit the channel length limit; only the first %d items were delivered, the remaining ones are held for the next batch", delivered)); err != nil {
				log.Printf("[notify] failed to queue segmented continuation channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(rest), err)
			}
		}
		return
	}
	if err == nil {
		// The channel neither returned an error nor reported how many items were delivered. Treat it as a failure (with backoff),
		// so this delivery doesn't keep getting claimed without ever being marked.
		err = fmt.Errorf("channel did not report delivered count (delivered=%d)", delivered)
	}

	// Failure handling is decided **per item**, not by the batch's maximum attempt count.
	//
	// It used to be `if maxAttempts(deliveries) >= MaxNotifyAttempts` failing the whole batch, but the attempts of items
	// within a batch differ: an old delivery already retried twice (attempts=2) would drag brand-new deliveries
	// (attempts=1) in the same batch into failed - new findings permanently lost without a single retry,
	// exactly the opposite of the intent "don't let old rows drag new rows down".
	permanent := notify.IsPermanent(err)
	var failIDs, exhaustedIDs []int64
	byDelay := map[time.Duration][]int64{}
	for _, dl := range deliveries {
		switch {
		case permanent:
			failIDs = append(failIDs, dl.ID)
		case dl.Attempts >= db.MaxNotifyAttempts:
			exhaustedIDs = append(exhaustedIDs, dl.ID)
		default:
			delay := notifyBackoff[min(dl.Attempts, len(notifyBackoff)-1)]
			byDelay[delay] = append(byDelay[delay], dl.ID)
		}
	}

	if len(failIDs) > 0 {
		if fErr := n.pg.FailDeliveries(ctx, failIDs, err.Error()); fErr != nil {
			log.Printf("[notify] error marking failed state channel=%s ids=%v: %v", channel.Kind(), failIDs, fErr)
		}
	}
	if len(exhaustedIDs) > 0 {
		reason := fmt.Sprintf("still failing after %d retries: %s", db.MaxNotifyAttempts, err)
		if fErr := n.pg.FailDeliveries(ctx, exhaustedIDs, reason); fErr != nil {
			log.Printf("[notify] error marking failed state channel=%s ids=%v: %v", channel.Kind(), exhaustedIDs, fErr)
		}
	}
	// Reschedule grouped by delay: there are only 3 backoff levels, so the number of groups is naturally small and there's no need to send
	// an UPDATE per item (which would cost a 500-item batch 500 round trips).
	for delay, group := range byDelay {
		if rErr := n.pg.RescheduleDeliveries(ctx, group, delay, err.Error()); rErr != nil {
			log.Printf("[notify] failed to reschedule deliveries channel=%s ids=%v: %v", channel.Kind(), group, rErr)
		}
	}
	if len(failIDs)+len(exhaustedIDs) > 0 {
		log.Printf("[notify] delivery failed channel=%d kind=%s permanent=%d exhausted=%d retrying=%d: %s",
			deliveries[0].ChannelID, channel.Kind(), len(failIDs), len(exhaustedIDs), len(byDelay), err)
	}
}

// excludeDeliveries returns those in all that are not in keep (compared by pointer identity).
// Used to find deliveries that "didn't make it into the message" - they must be handled explicitly, not left in a gray zone.
func excludeDeliveries(all, keep []*db.NotificationDelivery) []*db.NotificationDelivery {
	inKeep := make(map[*db.NotificationDelivery]bool, len(keep))
	for _, dl := range keep {
		inKeep[dl] = true
	}
	var out []*db.NotificationDelivery
	for _, dl := range all {
		if !inKeep[dl] {
			out = append(out, dl)
		}
	}
	return out
}

// adapt gets the channel implementation and parses its config.
// ok=false means the kind is not registered; the delivery should fail outright rather than retry forever.
func (n *Notifier) adapt(ch *db.NotificationChannel) (notify.Channel, map[string]any, bool) {
	channel, ok := notify.Get(ch.Kind)
	if !ok {
		return nil, nil, false
	}
	var cfg map[string]any
	if len(ch.Config) > 0 {
		// On config parse failure use an empty map: the channel's own Validate will report "which field is missing",
		// an error that is more helpful to the user than a JSON parse error.
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return channel, cfg, true
}

// renderSingle renders a single-finding message.
func (n *Notifier) renderSingle(ctx context.Context, dl *db.NotificationDelivery, baseURL string) (notify.Message, error) {
	snap, err := parseSnapshot(dl)
	if err != nil {
		return notify.Message{}, err
	}
	item, err := n.itemFor(ctx, snap, baseURL)
	if err != nil {
		return notify.Message{}, err
	}
	return notify.Message{Items: []notify.Item{item}, HomeURL: baseURL}, nil
}

// renderBatch renders a digest message. Snapshots are parsed one by one - a single broken one is skipped
// without taking down the whole digest.
//
// The returned included corresponds **strictly one-to-one** with msg.Items (i-th delivery <-> i-th item).
// This correspondence is a hard requirement: the caller marks the first K deliveries as delivered based on "the channel reported fitting
// the first K items". If a broken snapshot were skipped here without removing that delivery from included,
// the indexes would shift - a broken item that should fail would be marked delivered, and a good item misjudged as undelivered.
// The broken ones are explicitly marked failed by the caller, see stepDigest.
func (n *Notifier) renderBatch(ctx context.Context, deliveries []*db.NotificationDelivery, baseURL string, windowMinutes int) (notify.Message, []*db.NotificationDelivery, error) {
	items := make([]notify.Item, 0, len(deliveries))
	included := make([]*db.NotificationDelivery, 0, len(deliveries))
	for _, dl := range deliveries {
		snap, err := parseSnapshot(dl)
		if err != nil {
			// A broken snapshot goes into neither the message nor included - its handling is the caller's responsibility
			// (explicitly marked failed, rather than slipping through among the "delivered").
			log.Printf("[notify] skipped unparseable snapshot in digest batch delivery=%d: %v", dl.ID, err)
			continue
		}
		item, err := n.itemFor(ctx, snap, baseURL)
		if err != nil {
			return notify.Message{}, nil, err
		}
		items = append(items, item)
		included = append(included, dl)
	}
	if len(items) == 0 {
		return notify.Message{}, nil, fmt.Errorf("none of the %d deliveries in the digest batch could be parsed", len(deliveries))
	}
	return notify.Message{
		Items:         items,
		Batch:         true,
		WindowMinutes: windowMinutes,
		HomeURL:       baseURL,
	}, included, nil
}

// itemFor renders an event snapshot into an item to push, resolving asset names and the detail deep link along the way.
func (n *Notifier) itemFor(ctx context.Context, snap notify.Snapshot, baseURL string) (notify.Item, error) {
	assets, err := n.pg.NotificationAssetNames(ctx, snap.AssetIDs)
	if err != nil {
		// A failure to resolve asset names shouldn't block the push: missing a name is far less bad than missing a notification;
		// the message just lacks an asset line.
		log.Printf("[notify] failed to resolve asset names finding=%d: %v", snap.FindingID, err)
	}
	item := notify.Item{
		FindingID:  snap.FindingID,
		Name:       snap.Name,
		VulnClass:  snap.VulnClass,
		Severity:   snap.Severity,
		Summary:    snap.Summary,
		Assets:     assets,
		FromStatus: snap.FromStatus,
		ToStatus:   snap.ToStatus,
	}
	if baseURL != "" {
		// The detail page route is web/src/app/(main)/function/findings/detail/page.tsx,
		// which reads the finding id from the id query parameter.
		item.DetailURL = fmt.Sprintf("%s/function/findings/detail?id=%d", baseURL, snap.FindingID)
	}
	return item, nil
}

// takeTokens takes **at most want** tokens from the channel's token bucket and returns how many were actually taken.
//
// One token = one message (one HTTP request). In realtime mode the caller passes however many it wants;
// in digest mode a whole batch of findings sends only one message, so it passes 1.
//
// The bucket capacity is the channel's per-minute limit, refilled at a constant rate. ratePerMin<=0 means no rate limit,
// returning a finite but large enough value so a single loop isn't dragged down by an unbounded backlog.
//
// The want cap is required: without it the bucket could only be drained entirely, while the caller has its own per-tick cap,
// so extra tokens would be unusable and vanish before the next refill - the accumulated burst capacity would never be reachable,
// and even "no pending deliveries this round" would still be charged.
func (n *Notifier) takeTokens(channelID int64, ratePerMin, want int, now time.Time) int {
	if want <= 0 {
		return 0
	}
	if ratePerMin <= 0 {
		return min(want, notifyUnlimitedBurstPerTick)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	b := n.buckets[channelID]
	if b == nil {
		b = &notifyBucket{tokens: float64(ratePerMin), lastFill: now}
		n.buckets[channelID] = b
	}
	// Refill based on real elapsed time, at a rate of ratePerMin/60 per second.
	if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		b.tokens = minF(float64(ratePerMin), b.tokens+elapsed*float64(ratePerMin)/60)
		b.lastFill = now
	}
	// Add a tiny epsilon before truncating: the token count is accumulated in floating point, and refilling in two halves
	// 0.5 + 0.5 may yield 0.9999999999, which a direct int() truncates to 0 -
	// a mathematically full bucket would yield no token. 1e-9 is far smaller than one token, so no real deficit slips through.
	take := min(int(b.tokens+1e-9), want)
	if take <= 0 {
		return 0
	}
	b.tokens -= float64(take)
	return take
}

// enabled reads the master switch.
func (n *Notifier) enabled() bool {
	return n.pg.GetBool(settingNotifyEnabled, true)
}

// publicBaseURL returns the external URL for deep links, with the trailing slash removed.
func (n *Notifier) publicBaseURL() string {
	v, ok, err := n.pg.GetSetting(settingNotifyPublicBaseURL)
	if err != nil || !ok {
		return ""
	}
	return trimTrailingSlash(v)
}

// digestInterval returns the digest period, falling back to the default when invalid or unset.
func (n *Notifier) digestInterval() time.Duration {
	v, ok, err := n.pg.GetSetting(settingNotifyDigestMinutes)
	if err != nil || !ok {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	m := 0
	if _, err := fmt.Sscanf(v, "%d", &m); err != nil || m <= 0 {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	return time.Duration(m) * time.Minute
}

// parseSnapshot parses the snapshot of the event behind a delivery.
func parseSnapshot(dl *db.NotificationDelivery) (notify.Snapshot, error) {
	var snap notify.Snapshot
	if len(dl.Snapshot) == 0 {
		return snap, fmt.Errorf("event snapshot for delivery %d is empty", dl.ID)
	}
	if err := json.Unmarshal(dl.Snapshot, &snap); err != nil {
		return snap, fmt.Errorf("failed to parse event snapshot for delivery %d: %w", dl.ID, err)
	}
	if snap.Kind == "" {
		// The event type comes from the event row; the copy in the snapshot may have been written by an older version.
		snap.Kind = dl.EventKind
	}
	return snap, nil
}

func deliveryIDs(deliveries []*db.NotificationDelivery) []int64 {
	out := make([]int64, 0, len(deliveries))
	for _, dl := range deliveries {
		out = append(out, dl.ID)
	}
	return out
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
