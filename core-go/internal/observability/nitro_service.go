package observability

import (
	"context"
	pb "github.com/ai-gateway/core/api/gateway/v1"
)

// GrpcNitroClient 封装了原有 gRPC 客户端，使其满足 NitroClient 接口。
type GrpcNitroClient struct {
	Client pb.AiLogicClient
}

func (g *GrpcNitroClient) CheckInput(ctx context.Context, prompt string) (string, error) {
	resp, err := g.Client.CheckInput(ctx, &pb.InputRequest{Prompt: prompt})
	if err != nil {
		return prompt, err
	}
	return resp.SanitizedPrompt, nil
}

func (g *GrpcNitroClient) CountTokens(ctx context.Context, model, text string) (int, error) {
	resp, err := g.Client.CountTokens(ctx, &pb.TokenRequest{Model: model, Text: text})
	if err != nil {
		return 0, err
	}
	return int(resp.Count), nil
}

func (g *GrpcNitroClient) Close() error {
	return nil // gRPC 连接由 main 负责关闭
}
