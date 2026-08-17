package model

import (
	"strconv"
	"strings"
)

type APIKey struct {
	ID                int     `json:"id" gorm:"primaryKey"`
	Name              string  `json:"name" gorm:"not null"`
	APIKey            string  `json:"api_key" gorm:"not null"`
	Enabled           bool    `json:"enabled" gorm:"default:true"`
	ExpireAt          int64   `json:"expire_at,omitempty"`
	MaxCost           float64 `json:"max_cost,omitempty"`
	MaxRPM            int     `json:"max_rpm,omitempty"`
	SupportedModels   string  `json:"supported_models,omitempty"`
	SupportedChannels string  `json:"supported_channels,omitempty"` // 逗号分隔的渠道 ID 白名单，空表示不限渠道
}

// SupportedChannelIDSet 解析 supported_channels 字段。
// restricted 为 false 表示未配置渠道白名单；为 true 时即使集合为空也保持白名单语义。
func (k APIKey) SupportedChannelIDSet() (allowed map[int]struct{}, restricted bool) {
	raw := strings.TrimSpace(k.SupportedChannels)
	if raw == "" {
		return nil, false
	}
	allowed = make(map[int]struct{})
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.Atoi(part)
		if err != nil || id <= 0 {
			continue
		}
		allowed[id] = struct{}{}
	}
	return allowed, true
}
