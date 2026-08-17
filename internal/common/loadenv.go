// Package common 提供后端各模块共享的基础设施。
//
// 本文件 loadenv.go 负责把 KEY=VALUE 格式的配置文件（.env）加载到环境变量，
// 供 Load() 读取，支持"指定配置文件启动"，免去本地开发每次手动 source .env。
package common

import (
	"fmt"
	"os"
	"strings"
)

// LoadEnvFile 解析 path 指向的配置文件，把其中的 KEY=VALUE 键值对写入环境变量。
//
// 解析规则：
//   - 跳过空行与以 # 开头的注释行；
//   - 以每行第一个 = 分隔键值，两侧空白被去除；
//   - 值两侧成对的单/双引号会被剥除；不做变量展开。
//
// 优先级：已存在（非空）的环境变量不会被覆盖，保证 compose/k8s 注入的
// 真实环境变量始终优先于文件值，便于按环境覆盖。
//
// 入参：
//   - path: 配置文件路径。
//
// 返回：
//   - int: 本次实际写入的环境变量个数（已存在的键不计入）。
//   - error: 文件不可读，或某行缺少 = / 键为空（错误信息含行号）。
func LoadEnvFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("读取配置文件失败: %w", err)
	}
	loaded := 0
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq <= 0 {
			return loaded, fmt.Errorf("配置文件 %s 第 %d 行格式非法（应为 KEY=VALUE）: %q", path, i+1, line)
		}
		key := strings.TrimSpace(line[:eq])
		value := trimQuotes(strings.TrimSpace(line[eq+1:]))
		if os.Getenv(key) == "" {
			os.Setenv(key, value)
			loaded++
		}
	}
	return loaded, nil
}

// trimQuotes 剥除字符串两侧成对的单引号或双引号，不成对时原样返回。
func trimQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
