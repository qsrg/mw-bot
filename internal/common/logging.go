// File logging.go: 结构化 JSON 日志与 request_id 注入中间件，对齐 Python logging.py。
package common

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

// jsonLogHandler 自定义 slog.Handler，输出单行 JSON 日志。
// 格式：{"timestamp":...,"level":...,"message":...,"fields":{...},"request_id":...}
type jsonLogHandler struct {
	mu     *sync.Mutex // 共享互斥锁（指针），WithAttrs/WithGroup 派生的 handler 复用同一把锁，避免并发日志行交错（M4）
	writer io.Writer
	level  slog.Level
	attrs  []slog.Attr // WithAttrs 预设的属性
}

// newJSONLogHandler 创建输出到 w 的 JSON 日志 handler，最低级别为 level。
func newJSONLogHandler(w io.Writer, level slog.Level) *jsonLogHandler {
	return &jsonLogHandler{mu: &sync.Mutex{}, writer: w, level: level}
}

// Enabled 判断给定级别是否应记录。
func (h *jsonLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

// Handle 将一条日志记录序列化为 JSON 并写入输出。
// 从 context 提取 request_id 注入日志，attrs 合并到 fields 字段。
func (h *jsonLogHandler) Handle(ctx context.Context, r slog.Record) error {
	fields := make(map[string]any, len(h.attrs)+r.NumAttrs())
	for _, a := range h.attrs {
		fields[a.Key] = normalizeLogValue(a.Value.Any())
	}
	r.Attrs(func(a slog.Attr) bool {
		fields[a.Key] = normalizeLogValue(a.Value.Any())
		return true
	})

	payload := map[string]any{
		"timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		"level":     r.Level.String(),
		"message":   r.Message,
	}
	if len(fields) > 0 {
		payload["fields"] = fields
	}
	if requestID := RequestIDFromContext(ctx); requestID != "" {
		payload["request_id"] = requestID
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if h.mu != nil {
		h.mu.Lock()
		defer h.mu.Unlock()
	}
	_, err = h.writer.Write(data)
	return err
}

// WithAttrs 返回附带预设属性的新 handler 实例（共享同一把锁）。
func (h *jsonLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	newAttrs = append(newAttrs, h.attrs...)
	newAttrs = append(newAttrs, attrs...)
	return &jsonLogHandler{
		mu:     h.mu,
		writer: h.writer,
		level:  h.level,
		attrs:  newAttrs,
	}
}

// WithGroup 返回新 handler（共享同一把锁）。简化实现：不完整支持分组嵌套，仅透传属性。
func (h *jsonLogHandler) WithGroup(_ string) slog.Handler {
	return &jsonLogHandler{
		mu:     h.mu,
		writer: h.writer,
		level:  h.level,
		attrs:  h.attrs,
	}
}

// normalizeLogValue 归一化日志属性值：error 类型转为其错误文本。
// 标准库 error 实现无导出字段，json.Marshal 会序列化为空对象 {}，
// 导致错误日志丢失实际原因（如网关 429 配额信息），故统一转为文本。
func normalizeLogValue(v any) any {
	if e, ok := v.(error); ok {
		return e.Error()
	}
	return v
}

// InitLogger 初始化全局 logger，输出结构化 JSON 到 stdout，级别 INFO。
func InitLogger() {
	h := newJSONLogHandler(os.Stdout, slog.LevelInfo)
	logger := slog.New(h)
	slog.SetDefault(logger)
}

// RequestContextMiddleware 为每个请求生成或透传 request_id，注入 context 并写入响应头。
// 若请求头含 X-Request-ID 则透传，否则生成完整 UUID（对齐 Python logging.py 的 uuid4，M5）。
func RequestContextMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = uuid.New().String()
		}
		ctx := WithRequestID(r.Context(), requestID)
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
