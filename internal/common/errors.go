// File errors.go: 统一错误码与 AppError 异常类型，对齐 Python errors.py。
package common

import (
	"encoding/json"
	"net/http"
	"strings"
)

// 错误码常量，与 Python app/common/errors.py 对齐。
const (
	ErrCodeForbidden    = "AUTH_001" // 权限不足（403）
	ErrCodeUnauthorized = "AUTH_002" // 未认证或登录过期（401）
	ErrCodeBusiness     = "BIZ_001"  // 业务错误（400）
	ErrCodeNotFound     = "BIZ_404"  // 资源不存在（404）
	ErrCodeSystem       = "SYS_001"  // 系统内部错误（500）
)

// AppError 应用统一异常，携带错误码、面向用户消息、HTTP 状态码与原始错误。
type AppError struct {
	Code       string // 错误码，如 BIZ_001、AUTH_001
	Message    string // 面向用户的中文错误信息
	HTTPStatus int    // HTTP 状态码
	Cause      error  // 原始错误，用于排查但不暴露给用户
}

// Error 实现 error 接口，返回错误消息（附带原始错误信息）。
func (e *AppError) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

// Unwrap 支持 errors.Is/errors.As 错误链解包。
func (e *AppError) Unwrap() error {
	return e.Cause
}

// Forbidden 构造权限不足错误（AUTH_001, 403）。message 为空时使用默认文案。
func Forbidden(message string) *AppError {
	if message == "" {
		message = "无权限执行该操作"
	}
	return &AppError{
		Code:       ErrCodeForbidden,
		Message:    message,
		HTTPStatus: http.StatusForbidden,
	}
}

// Unauthorized 构造未认证错误（AUTH_002, 401）。message 为空时使用默认文案。
func Unauthorized(message string) *AppError {
	if message == "" {
		message = "未登录或登录已过期"
	}
	return &AppError{
		Code:       ErrCodeUnauthorized,
		Message:    message,
		HTTPStatus: http.StatusUnauthorized,
	}
}

// BusinessError 构造业务错误（BIZ_001, 400）。
func BusinessError(message string) *AppError {
	return &AppError{
		Code:       ErrCodeBusiness,
		Message:    message,
		HTTPStatus: http.StatusBadRequest,
	}
}

// NotFound 构造资源不存在错误（BIZ_404, 404）。message 为空时使用默认文案。
func NotFound(message string) *AppError {
	if message == "" {
		message = "资源不存在"
	}
	return &AppError{
		Code:       ErrCodeNotFound,
		Message:    message,
		HTTPStatus: http.StatusNotFound,
	}
}

// SystemError 构造系统内部错误（SYS_001, 500），携带原始错误用于排查。
func SystemError(err error) *AppError {
	return &AppError{
		Code:       ErrCodeSystem,
		Message:    "系统内部错误",
		HTTPStatus: http.StatusInternalServerError,
		Cause:      err,
	}
}

// SystemErrorWithMessage 构造系统内部错误（SYS_001, 500）并指定面向用户的消息。
// 对齐 Python system_error(message) 的可自定义消息语义（如"工具调用超时"）。
func SystemErrorWithMessage(message string) *AppError {
	return &AppError{
		Code:       ErrCodeSystem,
		Message:    message,
		HTTPStatus: http.StatusInternalServerError,
	}
}

// errorDetail 错误响应体 detail 字段，与 Python {"detail":{"code":...,"message":...}} 对齐。
type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// errorResponse 错误响应体。
type errorResponse struct {
	Detail errorDetail `json:"detail"`
}

// WriteError 将 AppError 序列化为 JSON 响应写入 http.ResponseWriter。
// 响应格式：{"detail":{"code":...,"message":...}}
func WriteError(w http.ResponseWriter, err *AppError) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(err.HTTPStatus)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Detail: errorDetail{Code: err.Code, Message: err.Message},
	})
}

// MethodNotAllowed 写入 405 Method Not Allowed 响应。
// 对齐 FastAPI 对错误 HTTP 方法的自动 405（Go 端各 router 手动校验方法时调用，M1）。
func MethodNotAllowed(w http.ResponseWriter, allowed ...string) {
	if len(allowed) > 0 {
		w.Header().Set("Allow", strings.Join(allowed, ", "))
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusMethodNotAllowed)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Detail: errorDetail{Code: "METHOD_405", Message: "method not allowed"},
	})
}
