// Package ingestion 实现文档解析、文本分块与异步索引任务，
// 对齐 Python app/ingestion 模块。
//
// - parsers.go: 将 PDF/DOCX/Markdown 解析为纯文本
// - chunking.go: 递归字符分块（对齐 langchain RecursiveCharacterTextSplitter）
// - service.go: 后台索引服务（goroutine 池 + 状态机 + 重试 + 启动恢复）
package ingestion

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/ledongthuc/pdf"
)

// ParsedDocument 解析后的文档，含纯文本与来源元数据。
// 字段与 Python ParsedDocument dataclass 对齐。
type ParsedDocument struct {
	Text     string         `json:"text"`     // 纯文本内容
	Metadata map[string]any `json:"metadata"` // 来源元数据（file_name, parser）
}

// ParseDocument 根据文件类型解析为纯文本。
// 支持扩展名 .pdf、.docx、.md，以及 content_type 为 text/markdown、text/plain 的文本。
//
// 参数：
//   - fileName: 文件名，用于判断扩展名。
//   - contentType: 内容类型。
//   - fileObj: 文件二进制流（会被读取到内存）。
//
// 返回：
//   - ParsedDocument: 解析后的文档。
//   - error: 不支持的格式或解析失败。
func ParseDocument(fileName, contentType string, fileObj io.Reader) (ParsedDocument, error) {
	lower := strings.ToLower(fileName)
	switch {
	case strings.HasSuffix(lower, ".pdf"):
		return parsePDF(fileName, fileObj)
	case strings.HasSuffix(lower, ".docx"):
		return parseDOCX(fileName, fileObj)
	case strings.HasSuffix(lower, ".md"), contentType == "text/markdown", contentType == "text/plain":
		return parseMarkdown(fileName, fileObj)
	default:
		return ParsedDocument{}, fmt.Errorf("不支持的文档格式")
	}
}

// parsePDF 用 ledongthuc/pdf 解析 PDF。
// 各页文本用 "\n\n" 连接，与 Python pypdf 行为一致。
func parsePDF(fileName string, fileObj io.Reader) (ParsedDocument, error) {
	data, err := io.ReadAll(fileObj)
	if err != nil {
		return ParsedDocument{}, fmt.Errorf("read pdf: %w", err)
	}
	reader := bytes.NewReader(data)
	pdfReader, err := pdf.NewReader(reader, int64(len(data)))
	if err != nil {
		return ParsedDocument{}, fmt.Errorf("open pdf: %w", err)
	}
	pages := make([]string, 0, pdfReader.NumPage())
	for i := 1; i <= pdfReader.NumPage(); i++ {
		page := pdfReader.Page(i)
		if page.V.IsNull() {
			pages = append(pages, "")
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			pages = append(pages, "")
			continue
		}
		pages = append(pages, text)
	}
	return ParsedDocument{
		Text: strings.Join(pages, "\n\n"),
		Metadata: map[string]any{
			"file_name": fileName,
			"parser":    "ledongthuc/pdf",
		},
	}, nil
}

// parseDOCX 解析 .docx 文件（zip + word/document.xml）。
// 提取所有段落 <w:p> 下的 <w:t> 文本，段落间用 "\n" 连接，
// 与 Python python-docx 行为一致。
func parseDOCX(fileName string, fileObj io.Reader) (ParsedDocument, error) {
	data, err := io.ReadAll(fileObj)
	if err != nil {
		return ParsedDocument{}, fmt.Errorf("read docx: %w", err)
	}
	zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ParsedDocument{}, fmt.Errorf("open docx zip: %w", err)
	}
	var docXML []byte
	for _, f := range zipReader.File {
		if f.Name == "word/document.xml" {
			rc, err := f.Open()
			if err != nil {
				return ParsedDocument{}, fmt.Errorf("open document.xml: %w", err)
			}
			docXML, err = io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return ParsedDocument{}, fmt.Errorf("read document.xml: %w", err)
			}
			break
		}
	}
	if docXML == nil {
		return ParsedDocument{}, fmt.Errorf("document.xml not found in docx")
	}
	text := extractDOCXText(docXML)
	return ParsedDocument{
		Text: text,
		Metadata: map[string]any{
			"file_name": fileName,
			"parser":    "archive/zip+xml",
		},
	}, nil
}

// extractDOCXText 从 document.xml 提取文本。
// 简化策略：按 token 遍历 XML，遇到 <w:p> 开启新段落，遇到 <w:t> 的字符数据加入当前段落，
// 遇到 </w:p> 关闭段落并拼接到结果（段落间用 "\n" 分隔）。
// 与 python-docx 过滤空白段落行为一致。
func extractDOCXText(xmlData []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(xmlData))
	var result strings.Builder
	var paraText strings.Builder
	inParagraph := false
	inText := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				inParagraph = true
				paraText.Reset()
			case "t":
				inText = true
			case "tab":
				// <w:tab/> -> 制表符，对齐 python-docx paragraph.text（L12）
				if inParagraph {
					paraText.WriteString("\t")
				}
			case "br":
				// <w:br/> -> 段内换行，对齐 python-docx paragraph.text（L12）
				if inParagraph {
					paraText.WriteString("\n")
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "p":
				if inParagraph {
					if strings.TrimSpace(paraText.String()) != "" {
						if result.Len() > 0 {
							result.WriteString("\n")
						}
						result.WriteString(paraText.String())
					}
				}
				inParagraph = false
			case "t":
				inText = false
			}
		case xml.CharData:
			if inText && inParagraph {
				paraText.Write(t)
			}
		}
	}
	return result.String()
}

// parseMarkdown 读取 UTF-8 文本，原文作为检索文本。
// 与 Python markdown-it-py 行为一致：Python 版 MarkdownIt().parse(raw) 为无效校验，
// 原文返回；Go 版直接原文返回，保留 file_name 元数据。
func parseMarkdown(fileName string, fileObj io.Reader) (ParsedDocument, error) {
	raw, err := io.ReadAll(fileObj)
	if err != nil {
		return ParsedDocument{}, fmt.Errorf("read markdown: %w", err)
	}
	return ParsedDocument{
		Text: string(raw),
		Metadata: map[string]any{
			"file_name": fileName,
			"parser":    "raw",
		},
	}, nil
}
