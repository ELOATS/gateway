package dependencies

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/ai-gateway/core/api/gateway/v1"
	"github.com/ai-gateway/core/internal/config"
	"google.golang.org/grpc"
)

type stubNitro struct {
	sanitized string
	count     int
	checkErr  error
	countErr  error
}

func (s *stubNitro) CheckInput(_ context.Context, prompt string) (string, error) {
	if s.checkErr != nil {
		return "", s.checkErr
	}
	if s.sanitized != "" {
		return s.sanitized, nil
	}
	return prompt, nil
}

func (s *stubNitro) CountTokens(_ context.Context, _ string, _ string) (int, error) {
	if s.countErr != nil {
		return 0, s.countErr
	}
	return s.count, nil
}

func (s *stubNitro) Close() error { return nil }

type stubAiLogic struct {
	inputResp  *pb.InputResponse
	outputResp *pb.OutputResponse
	cacheResp  *pb.CacheResponse
	inputErr   error
	outputErr  error
	cacheErr   error
	lastInput  *pb.InputRequest
	lastOutput *pb.OutputRequest
	lastCache  *pb.CacheRequest
}

func (s *stubAiLogic) CheckInput(_ context.Context, in *pb.InputRequest, _ ...grpc.CallOption) (*pb.InputResponse, error) {
	s.lastInput = in
	if s.inputErr != nil {
		return nil, s.inputErr
	}
	if s.inputResp != nil {
		return s.inputResp, nil
	}
	return &pb.InputResponse{Safe: true, SanitizedPrompt: in.Prompt}, nil
}

func (s *stubAiLogic) CheckOutput(_ context.Context, in *pb.OutputRequest, _ ...grpc.CallOption) (*pb.OutputResponse, error) {
	s.lastOutput = in
	if s.outputErr != nil {
		return nil, s.outputErr
	}
	if s.outputResp != nil {
		return s.outputResp, nil
	}
	return &pb.OutputResponse{Safe: true, SanitizedText: in.ResponseText}, nil
}

func (s *stubAiLogic) GetCache(_ context.Context, in *pb.CacheRequest, _ ...grpc.CallOption) (*pb.CacheResponse, error) {
	s.lastCache = in
	if s.cacheErr != nil {
		return nil, s.cacheErr
	}
	if s.cacheResp != nil {
		return s.cacheResp, nil
	}
	return &pb.CacheResponse{Hit: false}, nil
}

func (s *stubAiLogic) CountTokens(_ context.Context, _ *pb.TokenRequest, _ ...grpc.CallOption) (*pb.TokenResponse, error) {
	return &pb.TokenResponse{Count: 1}, nil
}

func testConfig() *config.Config {
	return &config.Config{
		CacheTimeout:            time.Second,
		GuardrailNitroTimeout:   time.Second,
		GuardrailIntellTimeout:  time.Second,
		NitroFailureMode:        "fail_closed",
		PythonInputFailureMode:  "fail_open_with_audit",
		PythonOutputFailureMode: "fail_open_with_audit",
		PythonCacheFailureMode:  "fail_open_with_audit",
	}
}

func TestFacadeCheckInputUsesNitroThenPython(t *testing.T) {
	ai := &stubAiLogic{inputResp: &pb.InputResponse{Safe: true, SanitizedPrompt: "python-clean"}}
	nitro := &stubNitro{sanitized: "nitro-clean"}
	facade := NewFacade(ai, nitro, testConfig())

	outcome := facade.CheckInput(context.Background(), "original")

	if !outcome.Allowed {
		t.Fatalf("expected input to be allowed, got %+v", outcome)
	}
	if outcome.SanitizedPrompt != "python-clean" {
		t.Fatalf("expected python sanitized prompt, got %q", outcome.SanitizedPrompt)
	}
	if ai.lastInput == nil || ai.lastInput.Prompt != "nitro-clean" {
		t.Fatalf("expected python input check to receive nitro sanitized prompt, got %#v", ai.lastInput)
	}
}

func TestFacadeCheckInputFailsClosedWhenNitroUnavailable(t *testing.T) {
	cfg := testConfig()
	cfg.NitroFailureMode = "fail_closed"
	facade := NewFacade(&stubAiLogic{}, &stubNitro{checkErr: errors.New("down")}, cfg)

	outcome := facade.CheckInput(context.Background(), "original")

	if outcome.Allowed {
		t.Fatalf("expected nitro failure to block request, got %+v", outcome)
	}
	if outcome.ErrorCode != "guardrail_unavailable" {
		t.Fatalf("expected guardrail_unavailable, got %+v", outcome)
	}
}

func TestFacadeCheckInputDegradesWhenPythonUnavailableInFailOpenMode(t *testing.T) {
	cfg := testConfig()
	cfg.PythonInputFailureMode = "fail_open_with_audit"
	facade := NewFacade(&stubAiLogic{inputErr: context.DeadlineExceeded}, &stubNitro{sanitized: "nitro-clean"}, cfg)

	outcome := facade.CheckInput(context.Background(), "original")

	if !outcome.Allowed || !outcome.Degraded {
		t.Fatalf("expected degraded allow outcome, got %+v", outcome)
	}
	if outcome.SanitizedPrompt != "nitro-clean" {
		t.Fatalf("expected nitro sanitized prompt to be preserved, got %q", outcome.SanitizedPrompt)
	}
}

func TestFacadeCheckOutputFailClosedAndSanitizeModes(t *testing.T) {
	cfg := testConfig()
	cfg.PythonOutputFailureMode = "fail_closed"
	blocked := NewFacade(&stubAiLogic{outputErr: errors.New("down")}, nil, cfg)

	blockedOutcome := blocked.CheckOutput(context.Background(), "unsafe")
	if blockedOutcome.Allowed {
		t.Fatalf("expected output check to fail closed, got %+v", blockedOutcome)
	}

	sanitized := NewFacade(&stubAiLogic{outputResp: &pb.OutputResponse{Safe: false, SanitizedText: "masked"}}, nil, testConfig())
	sanitizedOutcome := sanitized.CheckOutput(context.Background(), "unsafe")
	if !sanitizedOutcome.Allowed || !sanitizedOutcome.Degraded {
		t.Fatalf("expected unsafe output to be sanitized with degraded flag, got %+v", sanitizedOutcome)
	}
	if sanitizedOutcome.SanitizedText != "masked" {
		t.Fatalf("expected sanitized text, got %q", sanitizedOutcome.SanitizedText)
	}
}

func TestFacadeGetCacheModesAndRequestShape(t *testing.T) {
	cfg := testConfig()
	ai := &stubAiLogic{cacheResp: &pb.CacheResponse{Hit: true, Response: "cached-result"}}
	facade := NewFacade(ai, nil, cfg)

	hit := facade.GetCache(context.Background(), "prompt", "gpt-4")
	if !hit.Hit || hit.Response != "cached-result" {
		t.Fatalf("expected cache hit, got %+v", hit)
	}
	if ai.lastCache == nil || ai.lastCache.Prompt != "prompt" || ai.lastCache.Model != "gpt-4" {
		t.Fatalf("expected cache request to carry prompt/model, got %#v", ai.lastCache)
	}

	cfg.PythonCacheFailureMode = "fail_closed"
	hardFailure := NewFacade(&stubAiLogic{cacheErr: errors.New("cache down")}, nil, cfg).GetCache(context.Background(), "prompt", "gpt-4")
	if !hardFailure.HardFailure {
		t.Fatalf("expected fail-closed cache error to become hard failure, got %+v", hardFailure)
	}
}

func TestFacadeCountTokensDelegatesToNitro(t *testing.T) {
	facade := NewFacade(nil, &stubNitro{count: 42}, testConfig())

	count, err := facade.CountTokens(context.Background(), "gpt-4", "hello")
	if err != nil {
		t.Fatalf("unexpected count error: %v", err)
	}
	if count != 42 {
		t.Fatalf("expected token count 42, got %d", count)
	}
}
