package gateway

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// Entry is one audit record: request and response METADATA only. Prompt
// and completion content are never written here - only which provider
// handled the request, how many tokens it used, what it cost, how long it
// took, and (on failure) the error class. A security/cost-accounting tool
// logging the prompts it's meant to be auditing would be exactly the kind
// of thing it should be catching elsewhere in this account's tools.
type Entry struct {
	Time             string  `json:"ts"`
	Provider         string  `json:"provider"`
	Model            string  `json:"model,omitempty"`
	StatusCode       int     `json:"status_code"`
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	DurationMS       int64   `json:"duration_ms"`
	// AttemptedProviders lists every provider tried, in order, including
	// the one that finally succeeded (or the last one, if all failed) -
	// this is what makes fallback visible in the audit trail.
	AttemptedProviders []string `json:"attempted_providers,omitempty"`
	Error              string   `json:"error,omitempty"`

	// PromptProofVerdict is the promptproof verdict for the scanned request
	// content ("suspicious"/"dangerous"), empty when nothing triggered.
	PromptProofVerdict string `json:"promptproof_verdict,omitempty"`
	// PromptProofScore is the promptproof aggregate score.
	PromptProofScore int `json:"promptproof_score,omitempty"`
	// PromptProofCategories lists the finding categories seen (metadata
	// only — never the matched content).
	PromptProofCategories []string `json:"promptproof_categories,omitempty"`
	// PromptProofBlocked reports that the request was rejected (403)
	// before any provider was tried because it met the threshold under the
	// block action.
	PromptProofBlocked bool `json:"promptproof_blocked,omitempty"`
	// PromptProofError records a scanner failure. The request is forwarded
	// unscanned (fail-open); the error is surfaced here.
	PromptProofError string `json:"promptproof_error,omitempty"`
}

// Auditor appends Entry records as JSONL. Safe for concurrent use. A
// write failure is counted, not fatal - the gateway keeps proxying
// requests even if the audit sink breaks (e.g. disk full), matching the
// "audit failures never block requests" convention used elsewhere in this
// account's tools (mcp-gateway-lite, toolcage).
type Auditor struct {
	mu      sync.Mutex
	w       io.Writer
	closer  io.Closer
	dropped int
}

// NewAuditor opens path for append (created 0600 if it doesn't exist).
func NewAuditor(path string) (*Auditor, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Auditor{w: f, closer: f}, nil
}

// NewAuditorWriter wraps an arbitrary writer (tests, or a future
// stdout/stderr sink) without a backing file.
func NewAuditorWriter(w io.Writer) *Auditor {
	return &Auditor{w: w}
}

// Log writes one entry. Failures increment Dropped rather than panicking
// or propagating - see the Auditor doc comment.
func (a *Auditor) Log(e Entry) {
	if e.Time == "" {
		e.Time = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(e)
	if err != nil {
		a.mu.Lock()
		a.dropped++
		a.mu.Unlock()
		return
	}
	b = append(b, '\n')

	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.w.Write(b); err != nil {
		a.dropped++
	}
}

// Dropped returns the number of entries that failed to write.
func (a *Auditor) Dropped() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dropped
}

// Close closes the underlying file, if there is one.
func (a *Auditor) Close() error {
	if a.closer == nil {
		return nil
	}
	return a.closer.Close()
}
