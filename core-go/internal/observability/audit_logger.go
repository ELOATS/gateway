package observability

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"
)

// AuditRecord 代表一条经过 AI 网关的完整业务审计日志（包含长文本）。
type AuditRecord struct {
	Timestamp time.Time `json:"timestamp"`
	RequestID string    `json:"request_id"`
	APIKey    string    `json:"api_key,omitempty"` // 掩码后的 Key 或 User ID
	Model     string    `json:"model"`
	Node      string    `json:"node"`
	Prompt    string    `json:"prompt"`
	Response  string    `json:"response"`
	Tokens    int       `json:"tokens"`
}

// AuditLogger 提供高性能、无阻塞的异步日志下沉。
type AuditLogger struct {
	sinkCh   chan *AuditRecord
	wg       sync.WaitGroup
	quit     chan struct{}
	logFile  *os.File
	jsonEnc  *json.Encoder
}

// NewAuditLogger 初始化合规审计记录器，将数据写入指定的落盘文件。
func NewAuditLogger(filePath string) (*AuditLogger, error) {
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return nil, err
	}

	logger := &AuditLogger{
		sinkCh:  make(chan *AuditRecord, 5000), // 缓冲区容量
		quit:    make(chan struct{}),
		logFile: file,
		jsonEnc: json.NewEncoder(file),
	}

	logger.wg.Add(1)
	go logger.worker()

	return logger, nil
}

// Log 异步投递一条审计记录，即使队列满也不会阻塞主协程，而是丢弃或报警（此处保护网关存活优先）。
func (l *AuditLogger) Log(record *AuditRecord) {
	select {
	case l.sinkCh <- record:
	default:
		slog.Error("合规审计缓冲队列已满，丢弃日志！", "request_id", record.RequestID)
	}
}

// worker 是后台刷新协程。
func (l *AuditLogger) worker() {
	defer l.wg.Done()
	for {
		select {
		case record := <-l.sinkCh:
			if err := l.jsonEnc.Encode(record); err != nil {
				slog.Error("合规审查记录序列化失败", "error", err)
			}
		case <-l.quit:
			// 排空遗留数据
			for len(l.sinkCh) > 0 {
				record := <-l.sinkCh
				l.jsonEnc.Encode(record)
			}
			l.logFile.Sync()
			l.logFile.Close()
			return
		}
	}
}

// Close 优雅关闭合规收集器，保证落盘。
func (l *AuditLogger) Close(ctx context.Context) error {
	close(l.quit)
	
	c := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(c)
	}()

	select {
	case <-c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// GlobalAuditLogger 全局实例
var GlobalAuditLogger *AuditLogger

// InitGlobalAuditLogger 方便在 main 中初始化
func InitGlobalAuditLogger(filePath string) error {
	l, err := NewAuditLogger(filePath)
	if err != nil {
		return err
	}
	GlobalAuditLogger = l
	return nil
}
