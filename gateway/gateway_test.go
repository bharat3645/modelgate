package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testConfig(providers ...Provider) *Config {
	return &Config{
		Listen:    "127.0.0.1:0",
		Audit:     AuditConfig{Path: "unused"},
		Providers: providers,
		Timeout:   TimeoutField{set: true, value: 5},
	}
}

func doChatRequest(t *testing.T, gw *Gateway, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)
	return rec.Result()
}

func TestGatewaySuccessOnFirstProvider(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-primary" {
			t.Errorf("Authorization header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`))
	}))
	defer upstream.Close()

	t.Setenv("PRIMARY_KEY", "sk-primary")
	cfg := testConfig(Provider{Name: "primary", BaseURL: upstream.URL, APIKeyEnv: "PRIMARY_KEY", Pricing: Pricing{PromptPer1M: 0.05, CompletionPer1M: 0.08}})
	var auditBuf bytes.Buffer
	gw := New(cfg, NewAuditorWriter(&auditBuf))

	resp := doChatRequest(t, gw, `{"model":"m","messages":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var entry Entry
	if err := json.Unmarshal(bytes.TrimSpace(auditBuf.Bytes()), &entry); err != nil {
		t.Fatalf("audit entry not valid JSON: %v (%s)", err, auditBuf.String())
	}
	if entry.Provider != "primary" || entry.PromptTokens != 100 || entry.CompletionTokens != 20 {
		t.Errorf("audit entry = %+v", entry)
	}
	wantCost := EstimateCostUSD(Usage{PromptTokens: 100, CompletionTokens: 20}, Pricing{PromptPer1M: 0.05, CompletionPer1M: 0.08})
	if !almostEqual(entry.CostUSD, wantCost) {
		t.Errorf("audit cost = %v, want %v", entry.CostUSD, wantCost)
	}
}

func TestGatewayFallsBackOn503(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"overloaded"}`))
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer fallback.Close()

	t.Setenv("PRIMARY_KEY", "sk-1")
	t.Setenv("FALLBACK_KEY", "sk-2")
	cfg := testConfig(
		Provider{Name: "primary", BaseURL: primary.URL, APIKeyEnv: "PRIMARY_KEY"},
		Provider{Name: "fallback", BaseURL: fallback.URL, APIKeyEnv: "FALLBACK_KEY"},
	)
	var auditBuf bytes.Buffer
	gw := New(cfg, NewAuditorWriter(&auditBuf))

	resp := doChatRequest(t, gw, `{"model":"m","messages":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (from fallback)", resp.StatusCode)
	}

	lines := strings.Split(strings.TrimSpace(auditBuf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d audit lines, want 2 (one failed attempt, one success)", len(lines))
	}
	var failed, succeeded Entry
	_ = json.Unmarshal([]byte(lines[0]), &failed)
	_ = json.Unmarshal([]byte(lines[1]), &succeeded)
	if failed.Provider != "primary" || failed.StatusCode != 503 {
		t.Errorf("first audit entry = %+v, want primary/503", failed)
	}
	if succeeded.Provider != "fallback" || succeeded.StatusCode != 200 {
		t.Errorf("second audit entry = %+v, want fallback/200", succeeded)
	}
	if len(succeeded.AttemptedProviders) != 2 {
		t.Errorf("success entry AttemptedProviders = %v, want [primary fallback]", succeeded.AttemptedProviders)
	}
}

func TestGatewayFallsBackOn429(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer fallback.Close()

	t.Setenv("PRIMARY_KEY", "sk-1")
	t.Setenv("FALLBACK_KEY", "sk-2")
	cfg := testConfig(
		Provider{Name: "primary", BaseURL: primary.URL, APIKeyEnv: "PRIMARY_KEY"},
		Provider{Name: "fallback", BaseURL: fallback.URL, APIKeyEnv: "FALLBACK_KEY"},
	)
	gw := New(cfg, NewAuditorWriter(&bytes.Buffer{}))
	resp := doChatRequest(t, gw, `{"model":"m","messages":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (429 must be retried)", resp.StatusCode)
	}
}

func TestGatewayDoesNotRetryNon429ClientErrors(t *testing.T) {
	primaryCalls := 0
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid request: missing messages"}`))
	}))
	defer primary.Close()
	fallbackCalls := 0
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer fallback.Close()

	t.Setenv("PRIMARY_KEY", "sk-1")
	t.Setenv("FALLBACK_KEY", "sk-2")
	cfg := testConfig(
		Provider{Name: "primary", BaseURL: primary.URL, APIKeyEnv: "PRIMARY_KEY"},
		Provider{Name: "fallback", BaseURL: fallback.URL, APIKeyEnv: "FALLBACK_KEY"},
	)
	gw := New(cfg, NewAuditorWriter(&bytes.Buffer{}))
	resp := doChatRequest(t, gw, `{"model":"m"}`)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (forwarded verbatim, not retried)", resp.StatusCode)
	}
	if primaryCalls != 1 {
		t.Errorf("primary called %d times, want 1", primaryCalls)
	}
	if fallbackCalls != 0 {
		t.Errorf("fallback called %d times, want 0 (a 400 should not trigger fallback)", fallbackCalls)
	}
}

func TestGatewayAllProvidersFailedReturns502WithAttemptedList(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fallback.Close()

	t.Setenv("PRIMARY_KEY", "sk-1")
	t.Setenv("FALLBACK_KEY", "sk-2")
	cfg := testConfig(
		Provider{Name: "primary", BaseURL: primary.URL, APIKeyEnv: "PRIMARY_KEY"},
		Provider{Name: "fallback", BaseURL: fallback.URL, APIKeyEnv: "FALLBACK_KEY"},
	)
	gw := New(cfg, NewAuditorWriter(&bytes.Buffer{}))
	resp := doChatRequest(t, gw, `{"model":"m","messages":[]}`)

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("502 body not valid JSON: %v", err)
	}
	attempted, ok := body["attempted_providers"].([]any)
	if !ok || len(attempted) != 2 {
		t.Errorf("attempted_providers = %v, want [primary fallback]", body["attempted_providers"])
	}
}

func TestGatewayRejectsStreamingRequests(t *testing.T) {
	t.Setenv("PRIMARY_KEY", "sk-1")
	cfg := testConfig(Provider{Name: "primary", BaseURL: "http://unused.invalid", APIKeyEnv: "PRIMARY_KEY"})
	gw := New(cfg, NewAuditorWriter(&bytes.Buffer{}))

	resp := doChatRequest(t, gw, `{"model":"m","messages":[],"stream":true}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a streaming request", resp.StatusCode)
	}
}

func TestGatewayRejectsWrongPath(t *testing.T) {
	t.Setenv("PRIMARY_KEY", "sk-1")
	cfg := testConfig(Provider{Name: "primary", BaseURL: "http://unused.invalid", APIKeyEnv: "PRIMARY_KEY"})
	gw := New(cfg, NewAuditorWriter(&bytes.Buffer{}))

	req := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestGatewayRejectsGET(t *testing.T) {
	t.Setenv("PRIMARY_KEY", "sk-1")
	cfg := testConfig(Provider{Name: "primary", BaseURL: "http://unused.invalid", APIKeyEnv: "PRIMARY_KEY"})
	gw := New(cfg, NewAuditorWriter(&bytes.Buffer{}))

	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestGatewayRejectsInvalidJSON(t *testing.T) {
	t.Setenv("PRIMARY_KEY", "sk-1")
	cfg := testConfig(Provider{Name: "primary", BaseURL: "http://unused.invalid", APIKeyEnv: "PRIMARY_KEY"})
	gw := New(cfg, NewAuditorWriter(&bytes.Buffer{}))

	resp := doChatRequest(t, gw, `not json`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestGatewayRejectsOversizedBody(t *testing.T) {
	t.Setenv("PRIMARY_KEY", "sk-1")
	cfg := testConfig(Provider{Name: "primary", BaseURL: "http://unused.invalid", APIKeyEnv: "PRIMARY_KEY"})
	gw := New(cfg, NewAuditorWriter(&bytes.Buffer{}))

	huge := `{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("a", MaxRequestBodyBytes+100) + `"}]}`
	resp := doChatRequest(t, gw, huge)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
}

func TestGatewayModelOverrideRewritesModelPerProvider(t *testing.T) {
	var seenModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		seenModel, _ = req["model"].(string)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer upstream.Close()

	t.Setenv("KEY", "sk-1")
	cfg := testConfig(Provider{Name: "p", BaseURL: upstream.URL, APIKeyEnv: "KEY", ModelOverride: "accounts/fireworks/models/gpt-oss-120b"})
	gw := New(cfg, NewAuditorWriter(&bytes.Buffer{}))

	resp := doChatRequest(t, gw, `{"model":"generic-name","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if seenModel != "accounts/fireworks/models/gpt-oss-120b" {
		t.Errorf("upstream saw model = %q, want the override", seenModel)
	}
}

func TestGatewayNetworkErrorTriggersFallback(t *testing.T) {
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer fallback.Close()

	t.Setenv("PRIMARY_KEY", "sk-1")
	t.Setenv("FALLBACK_KEY", "sk-2")
	cfg := testConfig(
		// Deliberately unreachable: nothing listens on this port.
		Provider{Name: "primary", BaseURL: "http://127.0.0.1:1", APIKeyEnv: "PRIMARY_KEY"},
		Provider{Name: "fallback", BaseURL: fallback.URL, APIKeyEnv: "FALLBACK_KEY"},
	)
	gw := New(cfg, NewAuditorWriter(&bytes.Buffer{}))
	resp := doChatRequest(t, gw, `{"model":"m","messages":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (network error on primary must fall back)", resp.StatusCode)
	}
}

func TestGatewayHonorsConfiguredTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer fast.Close()

	t.Setenv("SLOW_KEY", "sk-1")
	t.Setenv("FAST_KEY", "sk-2")
	cfg := &Config{
		Listen: "127.0.0.1:0", Audit: AuditConfig{Path: "unused"},
		Timeout: TimeoutField{set: true, value: 1}, // 1 real second, but slow sleeps only 300ms - see below
		Providers: []Provider{
			{Name: "slow", BaseURL: slow.URL, APIKeyEnv: "SLOW_KEY"},
			{Name: "fast", BaseURL: fast.URL, APIKeyEnv: "FAST_KEY"},
		},
	}
	// Use a short client timeout directly rather than sleeping a real
	// second in the test: build the Gateway then shrink its client timeout.
	gw := New(cfg, NewAuditorWriter(&bytes.Buffer{}))
	gw.client.Timeout = 50 * time.Millisecond

	resp := doChatRequest(t, gw, `{"model":"m","messages":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (slow provider should time out and fall back to fast)", resp.StatusCode)
	}
}
