package model

import "time"

// AutoRankSnapshot 自动排序(Auto)候选的性能快照，用于重启后恢复内存学习窗口。
// 由控制面任务周期性整体替换；同一 (group, channel, model) 仅保留一行。
type AutoRankSnapshot struct {
	ID            uint      `json:"id" gorm:"primaryKey"`
	GroupID       int       `json:"group_id" gorm:"not null;index:idx_auto_rank_group_channel_model,unique"`
	ChannelID     int       `json:"channel_id" gorm:"not null;index:idx_auto_rank_group_channel_model,unique"`
	ModelName     string    `json:"model_name" gorm:"not null;index:idx_auto_rank_group_channel_model,unique"`
	Samples       int       `json:"samples" gorm:"not null;default:0"`       // 窗口内有效样本数
	ProbeSamples  int       `json:"probe_samples" gorm:"not null;default:0"` // 其中来自主动探测的条数
	Failures      int       `json:"failures" gorm:"not null;default:0"`      // 窗口内失败数
	SuccessRate   float64   `json:"success_rate" gorm:"not null;default:0"`  // 成功率（冗余，便于展示）
	EWMALatencyMS float64   `json:"ewma_latency_ms" gorm:"not null;default:0"`
	EWMATTFBMS    float64   `json:"ewma_ttfb_ms" gorm:"not null;default:0"`
	// SampleTrail 窗口内样本时间序列(JSON, 从旧到新, 每条含 age_ms/成功/探测/耗时)。
	// 供重启后精确恢复窗口的时间分布与失败位置; 旧行/损坏数据为空, 恢复时回退近似重建。
	SampleTrail string    `json:"sample_trail" gorm:"type:text"`
	LastSeenAt    time.Time `json:"last_seen_at" gorm:"index"`
	UpdatedAt     time.Time `json:"updated_at"`
}
