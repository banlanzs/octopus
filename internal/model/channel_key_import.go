package model

import (
	"encoding/json"
	"strings"
)

// MaxChannelKeyImportBatchSize 限制单次批量导入的 Key 数量，
// 避免一次性写入过大导致请求超时或超过数据库参数上限。
const MaxChannelKeyImportBatchSize = 1000

// ChannelKeyImportRequest 渠道 API Key 批量导入请求。
// Content 与 Keys 二选一（也可同时提供，服务端会合并后去重）。
type ChannelKeyImportRequest struct {
	ID      int      `json:"id" binding:"required"`
	Content string   `json:"content,omitempty"`
	Keys    []string `json:"keys,omitempty"`
	Enabled *bool    `json:"enabled,omitempty"`
	Remark  string   `json:"remark,omitempty"`
}

// ChannelKeyImportResult 批量导入结果。
type ChannelKeyImportResult struct {
	Imported   int      `json:"imported"`
	Duplicated int      `json:"duplicated"`
	Channel    *Channel `json:"channel,omitempty"`
}

// ParseChannelKeyImportContent 解析批量导入文本。
// 支持换行、逗号、分号、Tab 分隔，以及 JSON 字符串数组（例如 ["sk-a","sk-b"]）。
// 返回去重后的 key 列表和被忽略的重复项数量。
func ParseChannelKeyImportContent(content string) ([]string, int) {
	var keys []string
	duplicates := 0
	seen := make(map[string]struct{})

	add := func(raw string) {
		key := NormalizeChannelKeyImportItem(raw)
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			duplicates++
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}

	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
			for _, item := range arr {
				add(item)
			}
			return keys, duplicates
		}
	}

	content = strings.ReplaceAll(content, "\r\n", "\n")
	for _, line := range strings.Split(content, "\n") {
		for _, part := range splitChannelKeyImportLine(line) {
			add(part)
		}
	}
	return keys, duplicates
}

// NormalizeChannelKeyImportItem 规范化单个 key：去掉首尾空白、匹配的引号与
// 常见的 "Bearer " 前缀。
func NormalizeChannelKeyImportItem(raw string) string {
	key := strings.TrimSpace(raw)
	if len(key) >= 2 {
		first, last := key[0], key[len(key)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') || (first == '`' && last == '`') {
			key = strings.TrimSpace(key[1 : len(key)-1])
		}
	}
	if len(key) >= 7 && strings.EqualFold(key[:7], "bearer ") {
		key = strings.TrimSpace(key[7:])
	}
	return key
}

// splitChannelKeyImportLine 按常见分隔符拆分一行导入内容。
// 同一行中若没有逗号/分号/Tab，则按空白拆分为多个 key（API key 本身不含空白）。
func splitChannelKeyImportLine(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(line), "bearer ") {
		return []string{line}
	}
	if strings.ContainsAny(line, ",;\t") {
		return strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == ';' || r == '\t'
		})
	}
	return strings.Fields(line)
}
