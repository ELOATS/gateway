package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/ai-gateway/core/api/gateway/v1"
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/nitro"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/ai-gateway/core/internal/router"
	"github.com/ai-gateway/core/pkg/models"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
)

type providerSpy struct {
	calls int
	resp  *models.ChatCompletionResponse
	err   error
}

func (p *providerSpy) ChatCompletion(ctx context.Context, _ *models.ChatCompletionRequest) (*models.ChatCompletionResponse, error) {
	p.calls++
	if p.resp != nil {
		return p.resp, p.err
	}
	return &models.ChatCompletionResponse{
		ID:    "resp-1",
		Model: "gpt-4",
		Choices: []models.Choice{{
			Index:        0,
			FinishReason: "stop",
			Message: models.Message{
				Role:    "assistant",
				Content: "hello",
			},
		}},
		Usage: models.Usage{TotalTokens: 12},
	}, p.err
}

func (p *providerSpy) ChatCompletionStream(ctx context.Context, _ *models.ChatCompletionRequest) (<-chan *models.ChatCompletionStreamResponse, <-chan error) {
	respCh := make(chan *models.ChatCompletionStreamResponse)
	errCh := make(chan error, 1)
	close(respCh)
	close(errCh)
	return respCh, errCh
}

type configurableNitro struct {
	checkErr error
}

func (c *configurableNitro) CheckInput(_ context.Context, prompt string) (string, error) {
	if c.checkErr != nil {
		return "", c.checkErr
	}
	return prompt, nil
}

func (c *configurableNitro) CountTokens(_ context.Context, _ string, _ string) (int, error) {
	return 1, nil
}

func (c *configurableNitro) Close() error { return nil }

type configurableAiLogicClient struct {
	checkInputResp *pb.InputResponse
	checkInputErr  error
	checkOutputErr error
}

func (c *configurableAiLogicClient) CheckInput(_ context.Context, in *pb.InputRequest, _ ...grpc.CallOption) (*pb.InputResponse, error) {
	if c.checkInputErr != nil {
		return nil, c.checkInputErr
	}
	if c.checkInputResp != nil {
		return c.checkInputResp, nil
	}
	return &pb.InputResponse{Safe: true, SanitizedPrompt: in.Prompt}, nil
}

func (c *configurableAiLogicClient) CheckOutput(_ context.Context, in *pb.OutputRequest, _ ...grpc.CallOption) (*pb.OutputResponse, error) {
	if c.checkOutputErr != nil {
		return nil, c.checkOutputErr
	}
	return &pb.OutputResponse{Safe: true, SanitizedText: in.ResponseText}, nil
}

func (c *configurableAiLogicClient) GetCache(_ context.Context, _ *pb.CacheRequest, _ ...grpc.CallOption) (*pb.CacheResponse, error) {
	return &pb.CacheResponse{Hit: false}, nil
}

func (c *configurableAiLogicClient) CountTokens(_ context.Context, _ *pb.TokenRequest, _ ...grpc.CallOption) (*pb.TokenResponse, error) {
	return &pb.TokenResponse{Count: 1}, nil
}

type singleNodeStrategy struct{}

func (s *singleNodeStrategy) Name() string { return "fixed" }

func (s *singleNodeStrategy) Select(_ *router.RouteContext, nodes []*router.ModelNode) *router.ModelNode {
	return nodes[0]
}

func newPipelineTestHandler(t *testing.T, provider *providerSpy, ic pb.AiLogicClient, nc nitro.NitroClient) *ChatHandler {
	t.Helper()

	node := &router.ModelNode{
		Name:    "unit-node",
		ModelID: "gpt-4",
		Adapter: provider,
		Enabled: true,
	}
	tracker := router.NewHealthTracker(0.3)
	sr := router.NewSmartRouter([]*router.ModelNode{node}, tracker, "fixed")
	sr.RegisterStrategy(&singleNodeStrategy{})

	return NewChatHandler(
		ic,
		nc,
		sr,
		nil,
		&config.Config{
			APIKeys: []config.APIKeyEntry{
				{Key: "sk-premium", Label: "premium"},
				{Key: "sk-free", Label: "free"},
			},
			RequestTimeout:         5 * time.Second,
			TokenCountTimeout:      time.Second,
			CacheTimeout:           time.Second,
			GuardrailNitroTimeout:  time.Second,
			GuardrailIntellTimeout: time.Second,
			TokenEstimationFactor:  4,
			MaxConcurrentRequests:  4,
		},
	)
}

func withAuditLogger(t *testing.T) func() []string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	logger, err := observability.NewAuditLogger(path)
	if err != nil {
		t.Fatalf("create audit logger: %v", err)
	}
	prev := observability.GlobalAuditLogger
	observability.GlobalAuditLogger = logger

	return func() []string {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := logger.Close(ctx); err != nil {
			t.Fatalf("close audit logger: %v", err)
		}
		observability.GlobalAuditLogger = prev

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read audit file: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) == 1 && lines[0] == "" {
			return nil
		}
		return lines
	}
}

func newTestContext(body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	c.Set("request_id", "req-test")
	return c, w
}

func TestChatHandlerBlocksUnauthorizedToolsBeforeProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)

	provider := &providerSpy{}
	handler := newPipelineTestHandler(t, provider, &configurableAiLogicClient{}, nitro.NitroClient(&configurableNitro{}))
	readAudit := withAuditLogger(t)

	body, _ := json.Marshal(models.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []models.Message{{
			Role:    "user",
			Content: "use a tool",
		}},
		Tools: []models.Tool{{Type: "function"}},
	})

	c, w := newTestContext(body)
	c.Set("key_label", "free")
	c.Set("api_key", "sk-free")

	handler.HandleChatCompletions(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", w.Code, w.Body.String())
	}
	if provider.calls != 0 {
		t.Fatalf("expected provider not to be called, got %d calls", provider.calls)
	}

	lines := readAudit()
	if len(lines) == 0 || !strings.Contains(lines[0], `"event":"request_rejected"`) {
		t.Fatalf("expected rejection audit event, got %v", lines)
	}
}

func TestChatHandlerFailsClosedWhenNitroUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	provider := &providerSpy{}
	handler := newPipelineTestHandler(t, provider, &configurableAiLogicClient{}, nitro.NitroClient(&configurableNitro{checkErr: context.DeadlineExceeded}))

	body, _ := json.Marshal(models.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []models.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})

	c, w := newTestContext(body)
	c.Set("key_label", "premium")
	c.Set("api_key", "sk-premium")

	handler.HandleChatCompletions(c)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", w.Code, w.Body.String())
	}
	if provider.calls != 0 {
		t.Fatalf("expected provider not to be called, got %d calls", provider.calls)
	}
	if !strings.Contains(w.Body.String(), "guardrail_unavailable") {
		t.Fatalf("expected guardrail_unavailable response, got %s", w.Body.String())
	}
}

func TestChatHandlerDegradesWhenPythonGuardrailUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	provider := &providerSpy{}
	handler := newPipelineTestHandler(
		t,
		provider,
		&configurableAiLogicClient{checkInputErr: context.DeadlineExceeded},
		nitro.NitroClient(&configurableNitro{}),
	)
	readAudit := withAuditLogger(t)

	body, _ := json.Marshal(models.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []models.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})

	c, w := newTestContext(body)
	c.Set("key_label", "premium")
	c.Set("api_key", "sk-premium")

	handler.HandleChatCompletions(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if provider.calls != 1 {
		t.Fatalf("expected provider to be called once, got %d", provider.calls)
	}

	lines := readAudit()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `"event":"degraded"`) {
		t.Fatalf("expected degraded audit event, got %s", joined)
	}
	if !strings.Contains(joined, `"degraded":true`) {
		t.Fatalf("expected degraded audit flag, got %s", joined)
	}
}
