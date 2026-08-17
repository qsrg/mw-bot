// File tokens.go: 字符启发式 token 估算，对齐 Python tokens.py。
package common

// EstimateTokens 粗估文本 token 数，偏向高估以保安全。
// CJK 字符（Unicode 码点 > 127）按 2 token 计，ASCII 按 0.3 token 计。
// 算法与 Python tokens.py 完全一致：max(1, int(cjk*2 + ascii*0.3) + 1)。
// 对中文为主的混合内容略偏高估，对纯英文略偏低，作为预算护栏足够安全。
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	cjk := 0
	total := 0
	for _, ch := range text {
		total++
		if ch > 127 {
			cjk++
		}
	}
	asciiChars := total - cjk
	return max(1, int(float64(cjk)*2+float64(asciiChars)*0.3)+1)
}
