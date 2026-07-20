package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// MaxRequestBodyBytes bounds how much of a client request modelgate will
// buffer before forwarding. Chat-completion requests (even with long
// conversation history) are text; 10 MiB is generous headroom without
// letting an unbounded body exhaust memory.
const MaxRequestBodyBytes = 10 << 20 // 10 MiB

// resolvedProvider is a Provider with its API key already read from the
// environment once at startup, so a per-request proxy loop never touches
// os.Getenv (and so Config.Validate's "env var must be set" check and the
// actual proxying can't observe two different states of the environment).
type resolvedProvider struct {
	Provider
	apiKey string
}

// Gateway proxies /v1/chat/completions to the configured providers in
// order, falling back to the next one on a retryable failure.
type Gateway struct {
	providers []resolvedProvider
	auditor   *Auditor
	client    *http.Client
}

// New builds a Gateway from a loaded Config. Call Config.Validate (LoadConfig
// already does) before this - New re-resolves API keys from the
// environment but does not re-validate the rest of the config.
func New(cfg *Config, auditor *Auditor) *Gateway {
	providers := make([]resolvedProvider, len(cfg.Providers))
	for i, p := range cfg.Providers {
		providers[i] = resolvedProvider{Provider: p, apiKey: os.Getenv(p.APIKeyEnv)}
	}
	return &Gateway{
		providers: providers,
		auditor:   auditor,
		client:    &http.Client{Timeout: time.Duration(cfg.Timeout.Seconds()) * time.Second},
	}
}

// chatRequest is the minimal shape of an OpenAI-compatible chat-completions
// request that modelgate needs to inspect - not the full request, which is
// forwarded verbatim.
type chatRequest struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

// chatResponse is the minimal shape modelgate reads out of a successful
// response to log cost/usage - the full response body is still forwarded
// to the client byte-for-byte; this is a read, not a rewrite.
type chatResponse struct {
	Usage Usage `json:"usage"`
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" {
		http.Error(w, `{"error":"modelgate: only /v1/chat/completions is served in v0.1"}`, http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"modelgate: method not allowed, use POST"}`, http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBodyBytes+1))
	if err != nil {
		http.Error(w, `{"error":"modelgate: failed to read request body"}`, http.StatusBadRequest)
		return
	}
	if len(body) > MaxRequestBodyBytes {
		http.Error(w, `{"error":"modelgate: request body too large"}`, http.StatusRequestEntityTooLarge)
		return
	}

	var creq chatRequest
	if err := json.Unmarshal(body, &creq); err != nil {
		http.Error(w, `{"error":"modelgate: request body is not valid JSON"}`, http.StatusBadRequest)
		return
	}
	if creq.Stream {
		http.Error(w, `{"error":"modelgate: streaming requests (\"stream\": true) are not supported in v0.1 - see the roadmap"}`, http.StatusBadRequest)
		return
	}

	g.proxy(w, body)
}

// proxy tries each provider in order, forwarding body unmodified except
// for an optional per-provider ModelOverride. Stops at the first success
// or the first non-retryable client error (any 4xx except 429); a
// retryable failure (429, 5xx, network error, timeout) moves to the next
// provider. Every attempt is audited. If every provider is exhausted
// without success, the client gets a synthesized 502 naming every
// provider tried and the last failure - not just the last provider's raw
// response, which would look like an ordinary single-hop error and hide
// that fallback was attempted at all.
func (g *Gateway) proxy(w http.ResponseWriter, body []byte) {
	start := time.Now()
	var attempted []string
	var lastFailure string

	for _, p := range g.providers {
		attempted = append(attempted, p.Name)
		attemptStart := time.Now()

		outBody := body
		if p.ModelOverride != "" {
			overridden, err := overrideModel(body, p.ModelOverride)
			if err != nil {
				lastFailure = fmt.Sprintf("%s: applying model_override: %v", p.Name, err)
				continue
			}
			outBody = overridden
		}

		resp, respBody, err := g.attempt(p, outBody)
		duration := time.Since(attemptStart).Milliseconds()
		if err != nil {
			lastFailure = fmt.Sprintf("%s: %v", p.Name, err)
			g.auditor.Log(Entry{Provider: p.Name, DurationMS: duration, AttemptedProviders: append([]string(nil), attempted...), Error: err.Error()})
			continue
		}

		if resp.StatusCode == http.StatusOK {
			usage := parseUsage(respBody)
			cost := EstimateCostUSD(usage, p.Pricing)
			g.auditor.Log(Entry{
				Provider: p.Name, Model: p.ModelOverride, StatusCode: resp.StatusCode,
				PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
				CostUSD: cost, DurationMS: time.Since(start).Milliseconds(),
				AttemptedProviders: append([]string(nil), attempted...),
			})
			writeUpstreamResponse(w, resp, respBody)
			return
		}

		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		lastFailure = fmt.Sprintf("%s: upstream status %d", p.Name, resp.StatusCode)
		g.auditor.Log(Entry{
			Provider: p.Name, StatusCode: resp.StatusCode, DurationMS: duration,
			AttemptedProviders: append([]string(nil), attempted...),
			Error:              fmt.Sprintf("upstream status %d", resp.StatusCode),
		})
		if !retryable {
			// A 4xx (other than 429) will fail identically against every
			// provider - it's about the request, not the backend. Forward
			// it immediately instead of burning latency on doomed retries.
			writeUpstreamResponse(w, resp, respBody)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":               "modelgate: all providers failed",
		"attempted_providers": attempted,
		"last_failure":        lastFailure,
	})
}

func (g *Gateway) attempt(p resolvedProvider, body []byte) (*http.Response, []byte, error) {
	req, err := http.NewRequest(http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("request to %s: %w", p.Name, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("reading response from %s: %w", p.Name, err)
	}
	return resp, respBody, nil
}

func writeUpstreamResponse(w http.ResponseWriter, resp *http.Response, body []byte) {
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func parseUsage(respBody []byte) Usage {
	var cr chatResponse
	_ = json.Unmarshal(respBody, &cr) // best-effort: a provider that omits usage yields a zero Usage, not an error
	return cr.Usage
}

// overrideModel returns body with its top-level "model" field replaced,
// preserving every other field exactly. Requires body to already be valid
// JSON (ServeHTTP checks this before proxy is ever called).
func overrideModel(body []byte, model string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	m["model"] = model
	return json.Marshal(m)
}
