package testkit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"sync"
	"testing"

	platformlogger "github.com/v0hmly/marketmesh/platform/logger"
)

const maxLogEventBytes = 1024 * 1024

// LogEvent представляет одно декодированное структурное событие test logger.
// Каждый вызов Events возвращает независимые map, безопасные для изменения
// вызывающей стороной.
type LogEvent map[string]any

// LogCapture конкурентно-безопасно хранит JSON-события test logger.
type LogCapture struct {
	mu     sync.Mutex
	output bytes.Buffer
}

// NewLogger создаёт изолированный logger с безопасными настройками MarketMesh
// и конкурентно-безопасным захватом структурированных событий.
func NewLogger(t testing.TB) (*platformlogger.Logger, *LogCapture) {
	t.Helper()

	capture := &LogCapture{}
	log, err := platformlogger.New(platformlogger.Config{
		Service:     "test",
		Version:     "test",
		Environment: "test",
		Output:      capture,
	})
	if err != nil {
		t.Fatalf("testkit: create logger: %v", err)
	}
	t.Cleanup(capture.clear)

	return log, capture
}

// Write реализует io.Writer. Один LogCapture можно безопасно использовать из
// нескольких goroutine.
func (capture *LogCapture) Write(data []byte) (int, error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()

	return capture.output.Write(data)
}

// Events возвращает снимок всех полностью записанных JSON-событий.
func (capture *LogCapture) Events(t testing.TB) []LogEvent {
	t.Helper()

	data := capture.snapshot()
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), maxLogEventBytes)
	events := make([]LogEvent, 0)
	for scanner.Scan() {
		event := LogEvent{}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("testkit: decode log event: %v", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("testkit: scan log events: %v", err)
	}

	return events
}

// String возвращает согласованный снимок исходных JSON-строк.
func (capture *LogCapture) String() string {
	return string(capture.snapshot())
}

func (capture *LogCapture) snapshot() []byte {
	capture.mu.Lock()
	defer capture.mu.Unlock()

	return bytes.Clone(capture.output.Bytes())
}

func (capture *LogCapture) clear() {
	capture.mu.Lock()
	defer capture.mu.Unlock()

	data := capture.output.Bytes()
	clear(data)
	capture.output.Reset()
}
