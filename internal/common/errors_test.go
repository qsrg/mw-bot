// File errors_test.go: 错误码、HTTP 状态码与响应体序列化单元测试。
package common

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestErrorCodesAndHTTPStatus 验证各错误构造函数返回正确的错误码与 HTTP 状态码。
func TestErrorCodesAndHTTPStatus(t *testing.T) {
	cases := []struct {
		name       string
		err        *AppError
		wantCode   string
		wantStatus int
	}{
		{"Forbidden", Forbidden(""), ErrCodeForbidden, http.StatusForbidden},
		{"Unauthorized", Unauthorized(""), ErrCodeUnauthorized, http.StatusUnauthorized},
		{"BusinessError", BusinessError("参数错误"), ErrCodeBusiness, http.StatusBadRequest},
		{"NotFound", NotFound(""), ErrCodeNotFound, http.StatusNotFound},
		{"SystemError", SystemError(nil), ErrCodeSystem, http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.err.Code != c.wantCode {
				t.Errorf("Code 期望 %s, 实际 %s", c.wantCode, c.err.Code)
			}
			if c.err.HTTPStatus != c.wantStatus {
				t.Errorf("HTTPStatus 期望 %d, 实际 %d", c.wantStatus, c.err.HTTPStatus)
			}
		})
	}
}

// TestForbiddenDefaultMessage 验证 Forbidden 默认消息非空。
func TestForbiddenDefaultMessage(t *testing.T) {
	err := Forbidden("")
	if err.Message == "" {
		t.Error("Forbidden 默认消息不应为空")
	}
}

// TestSystemErrorCarriesCause 验证 SystemError 携带原始错误，可通过 Unwrap 解包。
func TestSystemErrorCarriesCause(t *testing.T) {
	inner := BusinessError("inner")
	err := SystemError(inner)
	if err.Cause == nil {
		t.Fatal("Cause 为空")
	}
	if err.Unwrap() != inner {
		t.Error("Unwrap 应返回原始错误")
	}
	// Error() 应包含原始错误信息
	if err.Error() == "" {
		t.Error("Error() 不应为空")
	}
}

// TestWriteErrorJSONFormat 验证 WriteError 输出 {"detail":{"code":...,"message":...}} 格式。
func TestWriteErrorJSONFormat(t *testing.T) {
	rec := httptest.NewRecorder()
	err := BusinessError("参数非法")
	WriteError(rec, err)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("HTTP 状态期望 400, 实际 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type 期望 application/json; charset=utf-8, 实际 %s", ct)
	}

	var body struct {
		Detail struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应体失败: %v", err)
	}
	if body.Detail.Code != ErrCodeBusiness {
		t.Errorf("响应 code 期望 %s, 实际 %s", ErrCodeBusiness, body.Detail.Code)
	}
	if body.Detail.Message != "参数非法" {
		t.Errorf("响应 message 期望 '参数非法', 实际 %s", body.Detail.Message)
	}
}

// TestAppErrorMessageWithoutCause 验证无 Cause 时 Error() 返回 Message。
func TestAppErrorMessageWithoutCause(t *testing.T) {
	err := BusinessError("简单错误")
	if err.Error() != "简单错误" {
		t.Errorf("Error() 期望 '简单错误', 实际 %s", err.Error())
	}
}
