package observability

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"
)

type AuditRecord struct {
	Timestamp time.Time `json:"timestamp"`
	RequestID string    `json:"request_id"`
	Event     string    `json:"event,omitempty"`
	Status    string    `json:"status,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Degraded  bool      `json:"degraded,omitempty"`
	APIKey    string    `json:"api_key,omitempty"`
	Model     string    `json:"model"`
	Node      string    `json:"node"`
	Prompt    string    `json:"prompt"`
	Response  string    `json:"response"`
	Tokens    int       `json:"tokens"`
}

type AuditLogger struct {
	sinkCh    chan *AuditRecord
	wg        sync.WaitGroup
	quit      chan struct{}
	closeOnce sync.Once
	mu        sync.RWMutex
	closed    bool
	logFile   *os.File
	jsonEnc   *json.Encoder
}

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
