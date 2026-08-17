// File middleware.go: 中间件识别工具（识别文档/问题涉及的中间件如 RocketMQ/Kafka），
// 对齐 Python middleware.py 的 URL 剔除、归一化、子串匹配与编辑距离容错算法。
package common

import (
	"regexp"
	"strings"
)

// urlRE 匹配 URL（http/https），识别前先剔除，避免文档链接中的中间件名误标。
var urlRE = regexp.MustCompile(`(?i)https?://\S+`)

// nonAlnumRE 匹配非字母数字字符，归一化时剔除空格/连字符/下划线等。
var nonAlnumRE = regexp.MustCompile(`[^a-z0-9]+`)

// MiddlewareList 返回配置的中间件注册表（去重、小写，按注册顺序）。
// 从环境变量 MIDDLEWARES 读取，未设置时使用默认中间件列表。
// 返回切片副本，避免调用方修改内部状态。
func MiddlewareList() []string {
	return getenvStringSlice("MIDDLEWARES", "rocketmq,kafka,rabbitmq,pulsar,redis,nacos")
}

// normalize 归一化文本：转小写并剔除非字母数字字符，容错空格/连字符差异。
func normalize(text string) string {
	return nonAlnumRE.ReplaceAllString(strings.ToLower(text), "")
}

// DetectMiddlewares 识别文本中出现的所有中间件，返回命中列表（小写，按注册表顺序）。
// 算法：先剔除 URL → 转小写 → 归一化 → 双轨匹配（原文子串 OR 归一化子串）。
// 一篇文档同时提及 Kafka 与 RocketMQ 时返回两个，支持多标记。
func DetectMiddlewares(text string) []string {
	if text == "" {
		return []string{}
	}
	low := strings.ToLower(urlRE.ReplaceAllString(text, ""))
	norm := normalize(low)
	hits := make([]string, 0)
	for _, mw := range MiddlewareList() {
		if strings.Contains(low, mw) || strings.Contains(norm, normalize(mw)) {
			hits = append(hits, mw)
		}
	}
	return hits
}

// MatchCandidate 从候选中间件中找出 text 最可能指向的一个，用于反问后的澄清回复匹配。
// 匹配策略（按优先级）：
//  1. 归一化子串匹配：text 归一化后包含候选归一化串（如 "用rocketmq" 命中 rocketmq）。
//  2. 编辑距离 ≤ 1：容错单字符错别字（如 "rockemq" 命中 "rocketmq"）。
//
// 返回命中的候选中间件名（小写）；均未匹配返回空串。
func MatchCandidate(text string, candidates []string) string {
	if text == "" || len(candidates) == 0 {
		return ""
	}
	normText := normalize(text)
	// 1. 归一化子串匹配
	for _, mw := range candidates {
		if strings.Contains(normText, normalize(mw)) {
			return strings.ToLower(mw)
		}
	}
	// 2. 编辑距离 ≤ 1 容错单字符错别字
	for _, mw := range candidates {
		if levenshteinLE1(normText, normalize(mw)) {
			return strings.ToLower(mw)
		}
	}
	return ""
}

// levenshteinLE1 判断两字符串编辑距离是否 ≤ 1（插入/删除/替换单字符）。
// 归一化后的字符串仅含 a-z0-9，使用字节比较即可。
// 长度差 > 1 直接返回 false；长度相等逐字符比较；长度差 1 用双指针扫描。
func levenshteinLE1(a, b string) bool {
	if absInt(len(a)-len(b)) > 1 {
		return false
	}
	if len(a) == len(b) {
		diff := 0
		for i := 0; i < len(a); i++ {
			if a[i] != b[i] {
				diff++
			}
		}
		return diff <= 1
	}
	// 长度差 1：短的串删一个字符应能与长的串对齐
	short, long := a, b
	if len(a) > len(b) {
		short, long = b, a
	}
	i, j, diff := 0, 0, 0
	for i < len(short) && j < len(long) {
		if short[i] == long[j] {
			i++
			j++
		} else {
			diff++
			if diff > 1 {
				return false
			}
			j++
		}
	}
	return true
}

// absInt 返回整数的绝对值。
func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
