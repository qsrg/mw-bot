// File middleware_test.go: 中间件识别单元测试，对齐 Python middleware.py 行为。
// 覆盖 URL 剔除、归一化、子串匹配、多中间件识别、编辑距离容错。
package common

import (
	"os"
	"reflect"
	"testing"
)

// withMiddlewares 临时设置 MIDDLEWARES 环境变量并在测试结束后恢复。
func withMiddlewares(t *testing.T, middlewares string) {
	t.Helper()
	old, had := os.LookupEnv("MIDDLEWARES")
	os.Setenv("MIDDLEWARES", middlewares)
	t.Cleanup(func() {
		if had {
			os.Setenv("MIDDLEWARES", old)
		} else {
			os.Unsetenv("MIDDLEWARES")
		}
	})
}

// TestMiddlewareListDefault 验证未设置环境变量时返回默认中间件列表。
func TestMiddlewareListDefault(t *testing.T) {
	withMiddlewares(t, "")
	got := MiddlewareList()
	want := []string{"rocketmq", "kafka", "rabbitmq", "pulsar", "redis", "nacos"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("默认列表期望 %v, 实际 %v", want, got)
	}
}

// TestMiddlewareListFromEnv 验证从环境变量读取并去重、转小写。
func TestMiddlewareListFromEnv(t *testing.T) {
	withMiddlewares(t, "RocketMQ, Kafka ,rocketmq, Redis")
	got := MiddlewareList()
	want := []string{"rocketmq", "kafka", "redis"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("期望 %v, 实际 %v", want, got)
	}
}

// TestDetectMiddlewaresBasic 验证文本中识别中间件名。
func TestDetectMiddlewaresBasic(t *testing.T) {
	withMiddlewares(t, "rocketmq,kafka,rabbitmq")
	text := "RocketMQ 与 Kafka 都是消息队列"
	got := DetectMiddlewares(text)
	want := []string{"rocketmq", "kafka"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("期望 %v, 实际 %v", want, got)
	}
}

// TestDetectMiddlewaresStripsURL 验证 URL 内中间件名不被误标。
// 如 "https://ai.nacos.io/config" 中 nacos 不应被识别（URL 已剔除）。
func TestDetectMiddlewaresStripsURL(t *testing.T) {
	withMiddlewares(t, "rocketmq,nacos,redis")
	text := "参考 https://ai.nacos.io/docs 配置 RocketMQ 集群"
	got := DetectMiddlewares(text)
	// nacos 仅出现在 URL 中，剔除后不应命中；rocketmq 在正文中应命中
	for _, mw := range got {
		if mw == "nacos" {
			t.Error("URL 内 nacos 不应被误标")
		}
	}
	if !contains(got, "rocketmq") {
		t.Error("正文 rocketmq 应被识别")
	}
}

// TestDetectMiddlewaresNormalization 验证空格/连字符归一化容错。
// "rocket mq" 与 "rocket-mq" 归一化后均命中 rocketmq。
func TestDetectMiddlewaresNormalization(t *testing.T) {
	withMiddlewares(t, "rocketmq")
	cases := []string{
		"rocket mq 集群",
		"rocket-mq 配置",
		"RocketMQ 部署",
	}
	for _, text := range cases {
		got := DetectMiddlewares(text)
		if !contains(got, "rocketmq") {
			t.Errorf("文本 %q 应通过归一化命中 rocketmq, 实际 %v", text, got)
		}
	}
}

// TestDetectMiddlewaresEmpty 验证空文本返回空切片。
func TestDetectMiddlewaresEmpty(t *testing.T) {
	withMiddlewares(t, "rocketmq")
	got := DetectMiddlewares("")
	if len(got) != 0 {
		t.Errorf("空文本期望空切片, 实际 %v", got)
	}
}

// TestMatchCandidateSubstring 验证归一化子串匹配优先。
// "用rocketmq处理" 归一化后包含 "rocketmq"。
func TestMatchCandidateSubstring(t *testing.T) {
	candidates := []string{"rocketmq", "kafka"}
	got := MatchCandidate("用rocketmq处理", candidates)
	if got != "rocketmq" {
		t.Errorf("期望 rocketmq, 实际 %s", got)
	}
}

// TestMatchCandidateTypo 验证编辑距离 ≤1 容错单字符错别字。
// "rockemq" 与 "rocketmq" 编辑距离 1（少一个 t）。
func TestMatchCandidateTypo(t *testing.T) {
	candidates := []string{"rocketmq"}
	got := MatchCandidate("rockemq", candidates)
	if got != "rocketmq" {
		t.Errorf("期望 rocketmq, 实际 %s", got)
	}
}

// TestMatchCandidateNoMatch 验证无匹配返回空串。
func TestMatchCandidateNoMatch(t *testing.T) {
	candidates := []string{"rocketmq", "kafka"}
	got := MatchCandidate("完全无关的文本", candidates)
	if got != "" {
		t.Errorf("无匹配期望空串, 实际 %s", got)
	}
}

// TestMatchCandidateEmptyInputs 验证空输入返回空串。
func TestMatchCandidateEmptyInputs(t *testing.T) {
	if got := MatchCandidate("", []string{"rocketmq"}); got != "" {
		t.Errorf("空文本期望空串, 实际 %s", got)
	}
	if got := MatchCandidate("rocketmq", nil); got != "" {
		t.Errorf("空候选期望空串, 实际 %s", got)
	}
}

// TestMatchCandidatePrefersFirstMatch 验证多候选时返回首个命中。
func TestMatchCandidatePrefersFirstMatch(t *testing.T) {
	candidates := []string{"rocketmq", "kafka"}
	// "rocketmq kafka" 同时命中两者，应返回列表中靠前的 rocketmq
	got := MatchCandidate("rocketmq kafka", candidates)
	if got != "rocketmq" {
		t.Errorf("期望 rocketmq, 实际 %s", got)
	}
}

// TestLevenshteinLE1 验证编辑距离 ≤1 判定。
func TestLevenshteinLE1(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"rocketmq", "rocketmq", true},  // 完全相同
		{"rocketmq", "rockemq", true},   // 删一个 t
		{"rocketmq", "rocketmqq", true}, // 加一个 q
		{"rocketmq", "rocketma", true},  // 替换 q→a
		{"rocketmq", "rockemqq", false}, // 删 t 加 q，距离 2
		{"abc", "xyz", false},           // 完全不同
		{"", "", true},                  // 空串
		{"a", "", true},                 // 长度差 1
		{"ab", "", false},               // 长度差 2
	}
	for _, c := range cases {
		got := levenshteinLE1(c.a, c.b)
		if got != c.want {
			t.Errorf("levenshteinLE1(%q, %q) 期望 %v, 实际 %v", c.a, c.b, c.want, got)
		}
	}
}
