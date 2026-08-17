package common

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// TestJSONLogHandler_ErrorRenderedAsText 验证 error 类型属性输出错误文本，
// 而不是被 json.Marshal 序列化为空对象 {}。
func TestJSONLogHandler_ErrorRenderedAsText(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newJSONLogHandler(&buf, slog.LevelInfo))

	logger.Error("模型网关流式调用失败", "error", errors.New("chat api status 429: quota exceeded"))

	out := buf.String()
	if !strings.Contains(out, "chat api status 429: quota exceeded") {
		t.Fatalf("错误文本未输出: %s", out)
	}
	if strings.Contains(out, `"error":{}`) {
		t.Fatalf("error 被序列化为空对象: %s", out)
	}
}

// TestJSONLogHandler_WrappedErrorRenderedAsText 验证 fmt.Errorf 包装的 error 同样输出完整文本。
func TestJSONLogHandler_WrappedErrorRenderedAsText(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newJSONLogHandler(&buf, slog.LevelInfo))

	wrapped := fmt.Errorf("call chat api: %w", errors.New("connection refused"))
	logger.Warn("工具决策调用失败", "error", wrapped)

	out := buf.String()
	if !strings.Contains(out, "call chat api: connection refused") {
		t.Fatalf("包装错误文本未输出: %s", out)
	}
}

// TestJSONLogHandler_WithAttrsErrorRenderedAsText 验证 WithAttrs 预设的 error 属性同样被归一化。
func TestJSONLogHandler_WithAttrsErrorRenderedAsText(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newJSONLogHandler(&buf, slog.LevelInfo)).
		With("error", errors.New("preset error text"))

	logger.Info("带预设属性的日志")

	out := buf.String()
	if !strings.Contains(out, "preset error text") {
		t.Fatalf("WithAttrs 的错误文本未输出: %s", out)
	}
}
