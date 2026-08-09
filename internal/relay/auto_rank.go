package relay

import (
	"context"

	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/utils/log"
)

// InitAutoRank 启动时从持久化快照恢复自动排序(Auto)内存学习窗口。
// 须在 op.InitCache 之后、task 启动之前调用；失败仅告警不影响启动。
func InitAutoRank(ctx context.Context) {
	snaps, err := op.AutoRankSnapshotListAll(ctx)
	if err != nil {
		log.Warnf("failed to load auto-rank snapshots: %v", err)
		return
	}
	if len(snaps) == 0 {
		return
	}
	balancer.AutoRankRestore(snaps)
	log.Infof("restored %d auto-rank snapshots", len(snaps))
}
