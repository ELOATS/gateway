package nitro

import (
	"context"

	pb "github.com/ai-gateway/core/api/gateway/v1"
	"google.golang.org/grpc"
)

type GrpcNitroClient struct {
	Client pb.AiLogicClient
	Conn   *grpc.ClientConn
}

// CheckInput 将输入护栏请求转发给远端 Nitro 服务。
// Go 主链路只消费返回值，不在这里掺杂额外策略判断。
func (g *GrpcNitroClient) CheckInput(ctx context.Context, prompt string) (string, error) {
	resp, err := g.Client.CheckInput(ctx, &pb.InputRequest{Prompt: prompt})
	if err != nil {
		return prompt, err
	}
	return resp.SanitizedPrompt, nil
}

// CountTokens 统一通过 Nitro 服务做模型相关的分词估算。
func (g *GrpcNitroClient) CountTokens(ctx context.Context, model, text string) (int, error) {
	resp, err := g.Client.CountTokens(ctx, &pb.TokenRequest{Model: model, Text: text})
	if err != nil {
		return 0, err
	}
	return int(resp.Count), nil
}

// Close 释放底层 gRPC 连接，避免进程退出前遗留长连接。
func (g *GrpcNitroClient) Close() error {
	if g.Conn != nil {
		return g.Conn.Close()
	}
	return nil
}
