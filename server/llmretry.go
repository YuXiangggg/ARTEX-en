package server

import (
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// Server-side parsing of the retry policy; see docs/LLM-retry-design.md. Of the five layers:
//   - connect / empty response / same-provider safety window "follow the endpoint": each LLM config can override
//     the global default (leaving a profile field blank inherits the global; if the global is unset too, the built-in default is used);
//   - circuit breaker / intent rerun are process-level, with only one global copy.
//
// The global policy reads one settings row from the DB; all call sites are low-frequency paths (building a provider, work wrap-up,
// saving config), so another cache layer is not worth it; the circuit-breaker parameters are the exception — they are read on every
// failure path, so applyRetryPolicy pushes them to the Registry to hold.

// retryPolicy reads the global policy; a nil DB yields the zero policy (all
// layers on their built-in defaults).
func (s *Server) retryPolicy() db.LLMRetryPolicy {
	if s.m == nil || s.m.pg == nil {
		return db.LLMRetryPolicy{}
	}
	return s.m.pg.LLMRetryPolicy()
}

// resolveRetry layers one profile's override on top of the global policy and
// converts the result into the form agent.Config carries. Rules combine field by
// field, so a profile that only pins an interval still inherits the global count.
func resolveRetry(o db.RetryOverride, pol db.LLMRetryPolicy) agent.RetryConfig {
	connect := o.Connect.Or(pol.Connect)
	empty := o.Empty.Or(pol.Empty)
	stream := o.Stream.Or(pol.Stream)
	return agent.RetryConfig{
		// Here the count keeps its original "0 = default / negative = disabled" semantics: the SDK's MaxRetries /
		// EmptyResponseRetries are exactly isomorphic to it, so let the SDK parse it itself.
		ConnectAttempts: connect.Attempts, ConnectInterval: connect.Interval(),
		EmptyAttempts: empty.Attempts, EmptyInterval: empty.Interval(),
		StreamAttempts: stream.Attempts, StreamInterval: stream.Interval(),
	}
}

// applyProfileRetry fills cfg.Retry for a profile read from the DB.
func (s *Server) applyProfileRetry(cfg *agent.Config, p *db.LLMProfile) {
	if p == nil {
		return
	}
	cfg.Retry = resolveRetry(p.Retry, s.retryPolicy())
}

// Circuit-breaker (round-robin cooldown) defaults, matching llmpool's built-ins — overridden here only when the user has configured a value.
// For intent-rerun defaults see modelErrorRetries / modelErrorRetryBackoff in engine.go.

// applyRetryPolicy pushes the process-wide layers of the policy into the objects
// that consume them on a hot path: the circuit-breaker registry. Called at
// startup and whenever the policy is saved.
func (s *Server) applyRetryPolicy() {
	pol := s.retryPolicy()
	if s.llmHealth != nil {
		s.llmHealth.SetPolicy(pol.Breaker.Attempts, pol.Breaker.Interval())
	}
}

// modelErrorRetryPolicy resolves the intent-level replay knobs (layer ⑤): how
// many times a model_error work is re-run and how long to back off between runs.
func (e *Engine) modelErrorRetryPolicy() (retries int, backoff time.Duration) {
	retries, backoff = modelErrorRetries, modelErrorRetryBackoff
	if e == nil || e.m == nil || e.m.pg == nil {
		return retries, backoff
	}
	rule := e.m.pg.LLMRetryPolicy().Intent
	if rule.Attempts != 0 {
		retries = max(rule.Attempts, 0)
	}
	if d := rule.Interval(); d > 0 {
		backoff = d
	}
	return retries, backoff
}

// emptyTurnNudgeLimit resolves how many empty-turn continuations one work may
// inject (see steerHooks.Stop). It deliberately reuses layer ②'s knob — the "empty-response
// retry count": the two are two means to the same end. The SDK layer handles "not a single content block", and its means is to
// resend the same request verbatim; here we handle "only thinking, with no body and no tools", and the means is to append an instruction telling
// the model to continue from its existing thinking (resending verbatim is pointless for this spin, which is determined by the shape of the context). The emptiness criteria
// differ because the SDK judges by "whether any event was yielded", and a thinking delta is itself an event — but when the user configures
// "how many times to retry an empty response", what they mean is "try again if the model produced no substantive content", so sharing one count across the two layers
// matches that mental model.
//
// Read the global policy rather than a specific profile's override: a run may switch profiles mid-way due to failover, and this
// is the total gate for the whole intent, which should not change with the endpoint. The semantics are isomorphic to the SDK's emptyRetries():
// 0 = default defaultEmptyTurnNudges; -1 (negative) = disable empty-spin continuation; >0 = use that value.
func (e *Engine) emptyTurnNudgeLimit() int {
	if e == nil || e.m == nil || e.m.pg == nil {
		return defaultEmptyTurnNudges
	}
	switch n := e.m.pg.LLMRetryPolicy().Empty.Attempts; {
	case n == 0:
		return defaultEmptyTurnNudges
	case n < 0:
		return 0
	default:
		return n
	}
}
