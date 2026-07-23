package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// benchMessage is the ~150-byte benign user message the scanning benchmark
// pushes through promptproof each iteration.
const benchMessage = "Please summarize the attached quarterly report and list the three " +
	"largest expense categories for the northeast region, with totals in USD."

func benchUpstream(b *testing.B) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`))
	}))
	b.Cleanup(srv.Close)
	return srv
}

// BenchmarkServeNoScan measures a chat-completion round trip through the
// gateway with scanning disabled — the baseline.
func BenchmarkServeNoScan(b *testing.B) {
	up := benchUpstream(b)
	b.Setenv("PP_KEY", "sk-x")
	cfg := testConfig(Provider{Name: "only", BaseURL: up.URL, APIKeyEnv: "PP_KEY", Pricing: Pricing{PromptPer1M: 0.05, CompletionPer1M: 0.08}})
	gw := New(cfg, NewAuditorWriter(discardWriter{}))
	body := []byte(chatWithMessage(benchMessage))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		gw.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	}
}

// BenchmarkServeWithPromptProof measures the same round trip with scanning
// enabled (benign content, warm coprocess pool). The delta against
// BenchmarkServeNoScan is the per-request overhead promptproof adds.
func BenchmarkServeWithPromptProof(b *testing.B) {
	bin := findPromptproofBin()
	if bin == "" {
		b.Skip("promptproof binary not found; set PROMPTPROOF_BIN")
	}
	up := benchUpstream(b)
	b.Setenv("PP_KEY", "sk-x")
	cfg := testConfig(Provider{Name: "only", BaseURL: up.URL, APIKeyEnv: "PP_KEY", Pricing: Pricing{PromptPer1M: 0.05, CompletionPer1M: 0.08}})
	cfg.PromptProof = &PromptProofConfig{Enabled: true, Binary: bin, Action: "block", Threshold: "dangerous", Pool: 4}
	gw := New(cfg, NewAuditorWriter(discardWriter{}))
	if err := gw.EnableScanning(cfg); err != nil {
		b.Fatal(err)
	}
	defer gw.Close()
	body := []byte(chatWithMessage(benchMessage))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		gw.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
	}
}

// BenchmarkScannerScan isolates just the coprocess round trip (frame out,
// verdict in) with no HTTP in the path.
func BenchmarkScannerScan(b *testing.B) {
	bin := findPromptproofBin()
	if bin == "" {
		b.Skip("promptproof binary not found; set PROMPTPROOF_BIN")
	}
	sc, err := newScanner(&PromptProofConfig{Enabled: true, Binary: bin, Pool: 1})
	if err != nil {
		b.Fatal(err)
	}
	defer sc.Close()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := sc.Scan(benchMessage); err != nil {
			b.Fatal(err)
		}
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
