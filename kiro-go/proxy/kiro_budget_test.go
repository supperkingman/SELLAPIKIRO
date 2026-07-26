package proxy

import (
	"testing"
	"time"
)

// TestKiroBudgetExhausted covers the wall-clock ceiling on a whole request.
//
// The retry layers multiply: accounts x endpoints x attempts per endpoint, each of
// which may wait out a first-frame timeout. Nothing bounded the total before this,
// and because the keepalive pings every 12s indefinitely the client sees traffic and
// keeps waiting rather than failing fast. A customer reported 36m41s of "thinking"
// with no chunk ever delivered.
func TestKiroBudgetExhausted(t *testing.T) {
	t.Run("fresh request has budget", func(t *testing.T) {
		if kiroBudgetExhausted(time.Now()) {
			t.Error("a request that just started must not be over budget")
		}
	})

	t.Run("long-running request is over budget", func(t *testing.T) {
		started := time.Now().Add(-kiroRequestBudget - time.Second)
		if !kiroBudgetExhausted(started) {
			t.Error("a request past the budget must report exhausted")
		}
	})

	t.Run("just inside the budget still runs", func(t *testing.T) {
		started := time.Now().Add(-kiroRequestBudget + 30*time.Second)
		if kiroBudgetExhausted(started) {
			t.Error("a request with time left must not be cut short")
		}
	})

	t.Run("zero time is treated as no deadline", func(t *testing.T) {
		// A caller that never recorded a start time must not have every attempt
		// refused, which is what comparing against the zero value would do.
		if kiroBudgetExhausted(time.Time{}) {
			t.Error("an unset start time must not report exhausted")
		}
	})
}

// TestKiroRequestBudgetIsShorterThanWorstCaseRetries is the point of the budget: it
// must actually bind. If the retry layers can still outlast it, the ceiling is
// decorative and the customer-visible hang returns.
func TestKiroRequestBudgetIsShorterThanWorstCaseRetries(t *testing.T) {
	worstCase := time.Duration(maxAccountRetryAttempts) *
		time.Duration(maxStreamAttemptsPerEndpoint) *
		kiroStreamFirstFrameTimeout()

	if kiroRequestBudget >= worstCase {
		t.Errorf("budget %s does not bind: retries alone can reach %s",
			kiroRequestBudget, worstCase)
	}

	// It must also leave room for a legitimate slow prefill, otherwise the fix trades
	// a hang for cutting off large-context requests that would have succeeded.
	if kiroRequestBudget <= kiroStreamFirstFrameTimeout() {
		t.Errorf("budget %s leaves no room for a single first-frame wait of %s",
			kiroRequestBudget, kiroStreamFirstFrameTimeout())
	}
}
