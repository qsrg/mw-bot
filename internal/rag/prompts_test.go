package rag

import "testing"

// TestIsClarification 验证反问模板识别：模板消息命中，普通回复与残缺文本不命中。
func TestIsClarification(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "标准反问模板命中",
			content: clarificationPrefix + "Kafka、Redis" + clarificationSuffix,
			want:    true,
		},
		{
			name:    "普通模型回复不命中",
			content: "Redis 默认端口是 6379。",
			want:    false,
		},
		{
			name:    "仅前缀不命中",
			content: clarificationPrefix,
			want:    false,
		},
		{
			name:    "前后缀拼合但无候选（长度不足）不命中",
			content: clarificationPrefix + clarificationSuffix,
			want:    false,
		},
		{
			name:    "兜底文案不命中",
			content: FallbackAnswer,
			want:    false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsClarification(c.content); got != c.want {
				t.Fatalf("IsClarification(%q) = %v, want %v", c.content, got, c.want)
			}
		})
	}
}

// TestClarificationAccessors 验证对外暴露的前后缀访问器与内部常量一致。
func TestClarificationAccessors(t *testing.T) {
	if ClarificationPrefix() != clarificationPrefix {
		t.Fatalf("ClarificationPrefix() = %q, want %q", ClarificationPrefix(), clarificationPrefix)
	}
	if ClarificationSuffix() != clarificationSuffix {
		t.Fatalf("ClarificationSuffix() = %q, want %q", ClarificationSuffix(), clarificationSuffix)
	}
}
