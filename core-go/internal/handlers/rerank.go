package handlers

import (
	"net/http"

	"github.com/ai-gateway/core/internal/application/rerank"
	"github.com/ai-gateway/core/pkg/models"
	"github.com/gin-gonic/gin"
)

// RerankHandler 是处理语义重排序请求的网关入口。
//
// 设计意图：
// 在 RAG 架构中，初次检索到的文档可能存在噪音。该处理器调用重排序服务，
// 对候选文档进行语义相似度二次评分，从而显著提升下游大模型生成的准确性。
type RerankHandler struct {
	Service *rerank.Service // 封装了重排序的具体业务逻辑（如调用远端模型或本地核心）
}

func NewRerankHandler(svc *rerank.Service) *RerankHandler {
	return &RerankHandler{
		Service: svc,
	}
}

// HandleRerank 对应 POST /v1/rerank 端点。
// 它接收原始查询与待排文档，返回经模型打分并排序后的结果。
func (h *RerankHandler) HandleRerank(c *gin.Context) {
	var req models.RerankRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	resp, err := h.Service.Rerank(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}
