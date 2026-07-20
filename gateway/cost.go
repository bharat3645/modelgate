// Package gateway implements modelgate's proxy: routes OpenAI-compatible
// chat-completion requests to a configured list of providers, falling back
// to the next one on error, and estimates per-request cost from the
// response's token usage.
package gateway

// Usage mirrors the "usage" object OpenAI-compatible chat-completions
// responses include: {"usage": {"prompt_tokens": N, "completion_tokens": N,
// "total_tokens": N}}. Not every provider under every config sets it (a
// request that never reaches a provider, or a provider that omits usage,
// has PromptTokens == CompletionTokens == 0), so cost in that case is
// correctly zero rather than an error.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Pricing is USD per 1,000,000 tokens, matching how providers publish
// their price sheets - avoids the repeating-decimal noise of per-token
// USD prices (e.g. $0.05 / 1M is exact; $0.00000005 / token is not any
// more precise and is harder to read or type correctly in a config file).
type Pricing struct {
	PromptPer1M     float64 `json:"prompt_per_1m_usd"`
	CompletionPer1M float64 `json:"completion_per_1m_usd"`
}

// EstimateCostUSD computes the estimated cost of one request from its
// reported token usage and a provider's per-1M-token pricing. Pure and
// side-effect-free so it's trivially unit-testable without a network.
func EstimateCostUSD(usage Usage, pricing Pricing) float64 {
	promptCost := float64(usage.PromptTokens) / 1_000_000 * pricing.PromptPer1M
	completionCost := float64(usage.CompletionTokens) / 1_000_000 * pricing.CompletionPer1M
	return promptCost + completionCost
}
