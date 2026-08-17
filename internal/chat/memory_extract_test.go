package chat

import (
	"testing"

	"mw-bot/internal/rag"
)

// TestShouldExtractMemory 验证长期记忆提取跳过条件：
// 兜底回复与反问模板跳过，正常回复（含闲聊）保留提取。
func TestShouldExtractMemory(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "兜底回复跳过提取",
			content: rag.FallbackAnswer,
			want:    false,
		},
		{
			name:    "反问模板跳过提取",
			content: rag.ClarificationPrefix() + "Kafka、Redis" + rag.ClarificationSuffix(),
			want:    false,
		},
		{
			name:    "正常知识回答保留提取",
			content: "Redis 默认端口是 6379，生产环境建议开启 AOF。",
			want:    true,
		},
		{
			name:    "闲聊回答保留提取（answer_style 偏好经 chat 意图轮次表达）",
			content: "好的，以后我会用中文回答你。",
			want:    true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldExtractMemory(c.content); got != c.want {
				t.Fatalf("shouldExtractMemory(%q) = %v, want %v", c.content, got, c.want)
			}
		})
	}
}
