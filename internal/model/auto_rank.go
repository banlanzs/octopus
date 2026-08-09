package model

import "time"

// AutoRankSnapshot 自动排序(Auto)候选的性能快照，用于重启后恢复内存学习窗口。
// 由控制面任务周期性 upsert；同一 (group, channel, model) 仅保留一行。
type AutoRankSnapshot struct {
	ID            uint      `json:"id" gorm:"primaryKey"`
	GroupID       int       `json:"group_id" gorm:"not null;index:idx_auto_rank_group_channel_model,unique"`
	ChannelID     int       `json:"channel_id" gorm:"not null;index:idx_auto_rank_group_channel_model,unique"`
	ModelName     string    `json:"model_name" gorm:"not null;index:idx_auto_rank_group_channel_model,unique"`
	Samples       int       `json:"samples" gorm:"not null;default:0"`      // 窗口内有效样本数
	Failures      int       `json:"failures" gorm:"not null;default:0"`     // 窗口内失败数
	SuccessRate   float64   `json:"success_rate" gorm:"not null;default:0"` // 成功率（冗余，便于展示）
	EWMALatencyMS float64   `json:"ewma_latency_ms" gorm:"not null;default:0"`
	UpdatedAt     time.Time `json:"updated_at"`
}
