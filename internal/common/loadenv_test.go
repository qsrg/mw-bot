package common

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeEnvFile 在临时目录写入指定内容的配置文件并返回其路径。
func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入临时配置文件失败: %v", err)
	}
	return path
}

// TestLoadEnvFile_Basic 验证常规 KEY=VALUE、注释、空行解析与引号剥除。
func TestLoadEnvFile_Basic(t *testing.T) {
	content := "# 注释行\n" +
		"\n" +
		"LOADENV_TEST_FOO=bar\n" +
		"LOADENV_TEST_QUOTED=\"hello world\"\n" +
		"LOADENV_TEST_SINGLE='abc'\n" +
		"  LOADENV_TEST_SPACED =  spaced value  \n"
	path := writeEnvFile(t, content)

	loaded, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("LoadEnvFile 返回错误: %v", err)
	}
	if loaded != 4 {
		t.Fatalf("期望写入 4 个键，实际 %d", loaded)
	}
	cases := map[string]string{
		"LOADENV_TEST_FOO":    "bar",
		"LOADENV_TEST_QUOTED": "hello world",
		"LOADENV_TEST_SINGLE": "abc",
		"LOADENV_TEST_SPACED": "spaced value",
	}
	for key, want := range cases {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q，期望 %q", key, got, want)
		}
	}
}

// TestLoadEnvFile_DoesNotOverrideExisting 验证真实环境变量优先于文件值。
func TestLoadEnvFile_DoesNotOverrideExisting(t *testing.T) {
	t.Setenv("LOADENV_TEST_EXISTING", "real-.env")
	path := writeEnvFile(t, "LOADENV_TEST_EXISTING=from-file\n")

	loaded, err := LoadEnvFile(path)
	if err != nil {
		t.Fatalf("LoadEnvFile 返回错误: %v", err)
	}
	if loaded != 0 {
		t.Fatalf("期望写入 0 个键，实际 %d", loaded)
	}
	if got := os.Getenv("LOADENV_TEST_EXISTING"); got != "real-.env" {
		t.Errorf("环境变量被文件覆盖: %q", got)
	}
}

// TestLoadEnvFile_MalformedLine 验证缺少 = 或键为空的行报错且含行号。
func TestLoadEnvFile_MalformedLine(t *testing.T) {
	path := writeEnvFile(t, "LOADENV_TEST_GOOD=1\nthis line has no equals sign\n")

	_, err := LoadEnvFile(path)
	if err == nil {
		t.Fatal("期望格式非法错误，实际为 nil")
	}
	if !strings.Contains(err.Error(), "第 2 行") {
		t.Errorf("错误信息应包含行号: %v", err)
	}

	pathEmptyKey := writeEnvFile(t, "=value-no-key\n")
	if _, err := LoadEnvFile(pathEmptyKey); err == nil {
		t.Fatal("期望空键错误，实际为 nil")
	}
}

// TestLoadEnvFile_Missing 验证文件不存在时返回错误。
func TestLoadEnvFile_Missing(t *testing.T) {
	if _, err := LoadEnvFile(filepath.Join(t.TempDir(), "no-such.env")); err == nil {
		t.Fatal("期望文件不存在错误，实际为 nil")
	}
}
