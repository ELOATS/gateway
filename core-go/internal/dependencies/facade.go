package dependencies

import (
	"context"
	"net/http"
	"strings"

	pb "github.com/ai-gateway/core/api/gateway/v1"
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/nitro"
)

// InputGuardOutcome 描述输入安全检查的最终结论。
// 它包含是否允许请求、经过脱敏后的 Prompt，以及在被拦截或降级时的元数据。
type InputGuardOutcome struct {
	Allowed         bool   // 是否允许请求通过
	SanitizedPrompt string // 经过过滤/脱敏后的 Prompt
	StatusCode      int    // 因安全原因拒绝时的 HTTP 响应码
	ErrorCode       string // 因安全原因拒绝时的错误码
	Message         string // 返回给用户的错误消息
	Reason          string // 内部记录的拦截详情（包含敏感原始词等）
	Degraded        bool   // 安全检查过程中是否触发了组件降级
	DegradeReason   string // 降级发生的具体原因
}

// OutputGuardOutcome 描述输出安全检查的最终结论。
type OutputGuardOutcome struct {
	Allowed       bool   // 是否允许输出通过
	SanitizedText string // 经过过滤/脱敏后的输出文本
	StatusCode    int    // 因安全原因拒绝时的 HTTP 响应码
	ErrorCode     string // 因安全原因拒绝时的错误码
	Message       string // 返回给用户的错误消息
	Reason        string // 内部记录的拦截详情
	Degraded      bool   // 输出层降级通常意味着审计失败但按配置“开路”放行
	DegradeReason string // 降级发生的具体原因
}

// CacheOutcome 描述语义缓存检索的结果。
type CacheOutcome struct {
	Hit           bool   // 是否命中缓存
	Response      string // 缓存的响应内容
	HardFailure   bool   // 是否发生致命错误（需中断请求）
	Degraded      bool   // 缓存检索是否降级
	DegradeReason string // 降级原因
}

// Facade 是网关内部复杂的依赖协调者。
// 
// 设计方案：
// 它封装了与 Nitro Wasm 核心、Python Sidecar (gRPC) 以及各 Cloud Provider 之间的交互。
// 主要目的是将 Pipeline 逻辑与具体的底层实现（如本地 Wasm 执行还是远端 gRPC 调用）解耦。
type Facade struct {
	intelligenceClient pb.AiLogicClient // 远程智能服务（Python/Rust 容器）
	nitroClient        nitro.NitroClient // 本地 Nitro 核心（通常基于 Wasm）
	config             *config.Config
}

// NewFacade 创建一个新的 Facade 实例。
func NewFacade(ic pb.AiLogicClient, nc nitro.NitroClient, cfg *config.Config) *Facade {
	return &Facade{
		intelligenceClient: ic,
		nitroClient:        nc,
		config:             cfg,
	}
}

// IntelligenceClient 返回远程智能服务客户端。
func (f *Facade) IntelligenceClient() pb.AiLogicClient {
	return f.intelligenceClient
}

// NitroClient 返回本地 Nitro 核心客户端。
func (f *Facade) NitroClient() nitro.NitroClient {
	return f.nitroClient
}

// CheckInput 执行多层级的输入内容安全检查。
// 流程：
// 1. 本地 Nitro 护栏预审（低延迟）。
// 2. 远程 Python Sidecar 深度分析（检测注入、越狱等复杂攻击）。
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

// CheckOutput 执行同步的输出内容安全审计。
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

// GetCache 尝试从远程智能服务获取语义缓存结果。
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
