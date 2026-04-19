package observability

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"
)

// AuditRecord 定义一条完整的审计日志项。
// 包含请求元数据、安全检测结果、路由信息以及消耗统计，用于后续的合规审计与成本对账。
type AuditRecord struct {
	Timestamp time.Time `json:"timestamp"`  // 事件发生时间
	RequestID string    `json:"request_id"` // 链路追踪 ID
	Event     string    `json:"event,omitempty"`
	Status    string    `json:"status,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Degraded  bool      `json:"degraded,omitempty"` // 是否触发了降级逻辑
	APIKey    string    `json:"api_key,omitempty"`  // 关联的租户 API Key（脱敏）
	Model     string    `json:"model"`              // 请求解析后的物理模型名
	Node      string    `json:"node"`               // 最终路由到的节点名
	Prompt    string    `json:"prompt"`             // 原始提示词
	Response  string    `json:"response"`           // 模型返回内容截断
	Tokens    int       `json:"tokens"`             // 实际消耗的总 Token 数
}

// AuditLogger 异步审计日志记录器。
//
// 设计意图：
// 1. 非阻塞：审计记录通过内存 Channel 异步下发，确保审计落盘逻辑不增加主请求延迟。
// 2. 可靠关闭：在系统关闭时通过 WaitGroup 和 CloseOnce 确保所有缓存日志均已落盘。
type AuditLogger struct {
	sinkCh    chan *AuditRecord // 记录下发的缓冲区
	wg        sync.WaitGroup
	quit      chan struct{}
	closeOnce sync.Once
	mu        sync.RWMutex
	closed    bool
	logFile   *os.File      // 后端持久化文件
	jsonEnc   *json.Encoder // JSON 序列化器
}

// NewAuditLogger 初始化异步审计日志记录器。
func NewAuditLogger(filePath string) (*AuditLogger, error) {
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		return nil, err
	}

	logger := &AuditLogger{
		sinkCh:  make(chan *AuditRecord, 5000),
		quit:    make(chan struct{}),
		logFile: file,
		jsonEnc: json.NewEncoder(file),
	}

	logger.wg.Add(1)
	go logger.worker()
	return logger, nil
}

func (l *AuditLogger) Log(record *AuditRecord) {
	if record == nil {
		return
	}

	l.mu.RLock()
	closed := l.closed
	l.mu.RUnlock()
	if closed {
		return
	}

	select {
	case l.sinkCh <- record:
	default:
		slog.Error("audit buffer full, dropping record", "request_id", record.RequestID)
		AuditDroppedTotal.Inc()
	}
}

func (l *AuditLogger) worker() {
	defer l.wg.Done()
	for {
		select {
		case record := <-l.sinkCh:
			if record == nil {
				continue
			}
			if err := l.jsonEnc.Encode(record); err != nil {
				slog.Error("audit record encode failed", "error", err)
			}
		case <-l.quit:
			for len(l.sinkCh) > 0 {
				record := <-l.sinkCh
				if record != nil {
					_ = l.jsonEnc.Encode(record)
				}
			}
			_ = l.logFile.Sync()
			_ = l.logFile.Close()
			return
		}
	}
}

func (l *AuditLogger) Close(ctx context.Context) error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closed = true
		l.mu.Unlock()
		close(l.quit)
	})

	done := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var GlobalAuditLogger *AuditLogger

func InitGlobalAuditLogger(filePath string) error {
	l, err := NewAuditLogger(filePath)
	if err != nil {
		return err
	}
	GlobalAuditLogger = l
	return nil
}
