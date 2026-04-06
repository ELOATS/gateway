package dependencies

import (
	"context"
	"net/http"
	"strings"

	pb "github.com/ai-gateway/core/api/gateway/v1"
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/nitro"
)

type InputGuardOutcome struct {
	Allowed         bool
	SanitizedPrompt string
	StatusCode      int
	ErrorCode       string
	Message         string
	Reason          string
	Degraded        bool
	DegradeReason   string
}

type OutputGuardOutcome struct {
	Allowed       bool
	SanitizedText string
	StatusCode    int
	ErrorCode     string
	Message       string
	Reason        string
	Degraded      bool
	DegradeReason string
}

type CacheOutcome struct {
	Hit           bool
	Response      string
	HardFailure   bool
	Degraded      bool
	DegradeReason string
}

type Facade struct {
	intelligenceClient pb.AiLogicClient
	nitroClient        nitro.NitroClient
	config             *config.Config
}

func NewFacade(ic pb.AiLogicClient, nc nitro.NitroClient, cfg *config.Config) *Facade {
	return &Facade{
		intelligenceClient: ic,
		nitroClient:        nc,
		config:             cfg,
	}
}

func (f *Facade) IntelligenceClient() pb.AiLogicClient {
	return f.intelligenceClient
}

func (f *Facade) NitroClient() nitro.NitroClient {
	return f.nitroClient
}

func (f *Facade) CheckInput(ctx context.Context, prompt string) InputGuardOutcome {
	sanitizedPrompt := prompt

	if f.nitroClient != nil {
		nitroCtx, cancelNitro := context.WithTimeout(ctx, f.config.GuardrailNitroTimeout)
		defer cancelNitro()

		sanitized, err := f.nitroClient.CheckInput(nitroCtx, prompt)
		if err != nil {
			if strings.EqualFold(f.config.NitroFailureMode, "fail_open_with_audit") {
				return InputGuardOutcome{
					Allowed:         true,
					SanitizedPrompt: prompt,
					Degraded:        true,
					DegradeReason:   "nitro input guardrail unavailable",
				}
			}
			return InputGuardOutcome{
				Allowed:    false,
				StatusCode: http.StatusServiceUnavailable,
				ErrorCode:  "guardrail_unavailable",
				Message:    "input guardrail unavailable",
				Reason:     "nitro safety check failed",
			}
		}
		sanitizedPrompt = sanitized
	}

	if f.intelligenceClient == nil {
		return InputGuardOutcome{Allowed: true, SanitizedPrompt: sanitizedPrompt}
	}

	pyCtx, cancelPy := context.WithTimeout(ctx, f.config.GuardrailIntellTimeout)
	defer cancelPy()

	pyResp, err := f.intelligenceClient.CheckInput(pyCtx, &pb.InputRequest{Prompt: sanitizedPrompt})
	if err != nil {
		if strings.EqualFold(f.config.PythonInputFailureMode, "fail_closed") {
			return InputGuardOutcome{
				Allowed:    false,
				StatusCode: http.StatusServiceUnavailable,
				ErrorCode:  "python_guardrail_unavailable",
				Message:    "python input guardrail unavailable",
				Reason:     "python input guardrail failure",
			}
		}
		return InputGuardOutcome{
			Allowed:         true,
			SanitizedPrompt: sanitizedPrompt,
			Degraded:        true,
			DegradeReason:   "python input guardrail unavailable",
		}
	}

	if !pyResp.Safe {
		return InputGuardOutcome{
			Allowed:    false,
			StatusCode: http.StatusForbidden,
			ErrorCode:  "security_block",
			Message:    pyResp.Reason,
			Reason:     pyResp.Reason,
		}
	}

	return InputGuardOutcome{Allowed: true, SanitizedPrompt: pyResp.SanitizedPrompt}
}

func (f *Facade) CheckOutput(ctx context.Context, text string) OutputGuardOutcome {
	if text == "" || f.intelligenceClient == nil {
		return OutputGuardOutcome{Allowed: true, SanitizedText: text}
	}

	pyCtx, cancelPy := context.WithTimeout(ctx, f.config.GuardrailIntellTimeout)
	defer cancelPy()

	pyResp, err := f.intelligenceClient.CheckOutput(pyCtx, &pb.OutputRequest{ResponseText: text})
	if err != nil {
		if strings.EqualFold(f.config.PythonOutputFailureMode, "fail_closed") {
			return OutputGuardOutcome{
				Allowed:       false,
				SanitizedText: text,
				StatusCode:    http.StatusServiceUnavailable,
				ErrorCode:     "python_output_guardrail_unavailable",
				Message:       "python output guardrail unavailable",
				Reason:        "python output guardrail failure",
			}
		}
		return OutputGuardOutcome{
			Allowed:       true,
			SanitizedText: text,
			Degraded:      true,
			DegradeReason: "python output guardrail unavailable",
		}
	}

	outcome := OutputGuardOutcome{Allowed: true, SanitizedText: pyResp.SanitizedText}
	if !pyResp.Safe {
		outcome.Degraded = true
		outcome.DegradeReason = "output sanitized by guardrail"
	}
	return outcome
}

func (f *Facade) GetCache(ctx context.Context, prompt, model string) CacheOutcome {
	if f.intelligenceClient == nil {
		return CacheOutcome{}
	}

	cacheCtx, cancel := context.WithTimeout(ctx, f.config.CacheTimeout)
	defer cancel()

	cacheResp, err := f.intelligenceClient.GetCache(cacheCtx, &pb.CacheRequest{Prompt: prompt, Model: model})
	if err != nil {
		if strings.EqualFold(f.config.PythonCacheFailureMode, "fail_closed") {
			return CacheOutcome{HardFailure: true, DegradeReason: "semantic cache unavailable"}
		}
		return CacheOutcome{Degraded: true, DegradeReason: "semantic cache unavailable"}
	}
	if !cacheResp.Hit {
		return CacheOutcome{}
	}

	return CacheOutcome{Hit: true, Response: cacheResp.Response}
}

func (f *Facade) CountTokens(ctx context.Context, model, text string) (int, error) {
	if f.nitroClient == nil {
		return 0, nil
	}
	return f.nitroClient.CountTokens(ctx, model, text)
}
