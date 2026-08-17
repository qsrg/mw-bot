// File tokens_test.go: token 估算函数单元测试，对齐 Python tokens.py。
package common

import "testing"

// TestEstimateTokensEmpty 验证空字符串返回 0。
func TestEstimateTokensEmpty(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("空字符串期望 0, 实际 %d", got)
	}
}

// TestEstimateTokensCJK 验证纯中文按 2 token/字估算。
// 5 个汉字：max(1, int(5*2 + 0*0.3) + 1) = max(1, 11) = 11
func TestEstimateTokensCJK(t *testing.T) {
	text := "你好世界吗" // 5 个 CJK 字符
	if got := EstimateTokens(text); got != 11 {
		t.Errorf("纯中文 5 字期望 11, 实际 %d", got)
	}
}

// TestEstimateTokensASCII 验证纯 ASCII 按 0.3 token/字估算。
// 5 个 ASCII：max(1, int(0*2 + 5*0.3) + 1) = max(1, int(1.5)+1) = max(1, 2) = 2
func TestEstimateTokensASCII(t *testing.T) {
	text := "hello" // 5 个 ASCII
	if got := EstimateTokens(text); got != 2 {
		t.Errorf("纯英文 5 字期望 2, 实际 %d", got)
	}
}

// TestEstimateTokensMixed 验证中英混合按各自系数累加。
// 3 中 + 2 英：max(1, int(3*2 + 2*0.3) + 1) = max(1, int(6.6)+1) = max(1, 7) = 7
func TestEstimateTokensMixed(t *testing.T) {
	text := "你好world" // 2 中 + 5 英 → max(1, int(2*2+5*0.3)+1) = max(1, int(5.5)+1) = 6
	if got := EstimateTokens(text); got != 6 {
		t.Errorf("2 中 + 5 英期望 6, 实际 %d", got)
	}
}

// TestEstimateTokensSingleChar 验证单字符至少返回 1。
func TestEstimateTokensSingleChar(t *testing.T) {
	if got := EstimateTokens("a"); got != 1 {
		t.Errorf("单 ASCII 期望 1, 实际 %d", got)
	}
	if got := EstimateTokens("你"); got != 3 {
		t.Errorf("单 CJK 期望 3 (int(1*2+0*0.3)+1=3), 实际 %d", got)
	}
}

// TestEstimateTokensAtLeastOne 验证非空文本至少返回 1。
func TestEstimateTokensAtLeastOne(t *testing.T) {
	// 纯标点：无 CJK 无 ASCII，total=0, cjk=0, asciiChars=0
	// max(1, int(0)+1) = 1
	if got := EstimateTokens("..."); got != 1 {
		t.Errorf("纯标点期望 1, 实际 %d", got)
	}
}
