// File helpers_test.go: Markdown 文档校验与文件名规范化的单元测试。
package knowledge

import "testing"

func TestIsMarkdown(t *testing.T) {
	cases := []struct {
		fileName    string
		contentType string
		want        bool
	}{
		{"RocketMQ-FAQ.md", "text/markdown", true},
		{"notes.MARKDOWN", "", true},
		{"doc.txt", "text/markdown", true},
		{"report", "text/plain", true},
		{"report.pdf", "application/pdf", false},
		{"report.docx", "", false},
		{"archive.zip", "application/zip", false},
	}
	for _, c := range cases {
		if got := isMarkdown(c.fileName, c.contentType); got != c.want {
			t.Errorf("isMarkdown(%q, %q) = %v, want %v", c.fileName, c.contentType, got, c.want)
		}
	}
}

func TestNormalizeMarkdownFileName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"FAQ", "FAQ.md"},
		{"FAQ.md", "FAQ.md"},
		{"FAQ.MD", "FAQ.MD"},
		{"FAQ.markdown", "FAQ.markdown"},
		{"常见问题", "常见问题.md"},
	}
	for _, c := range cases {
		if got := normalizeMarkdownFileName(c.in); got != c.want {
			t.Errorf("normalizeMarkdownFileName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsMarkdownDocument(t *testing.T) {
	doc := &Document{FileName: "a.md"}
	if !doc.IsMarkdownDocument() {
		t.Error("a.md 应识别为 Markdown")
	}
	doc = &Document{FileName: "b.bin", ContentType: "text/markdown"}
	if !doc.IsMarkdownDocument() {
		t.Error("text/markdown 应识别为 Markdown")
	}
	doc = &Document{FileName: "c.pdf", ContentType: "application/pdf"}
	if doc.IsMarkdownDocument() {
		t.Error("pdf 不应识别为 Markdown")
	}
}
