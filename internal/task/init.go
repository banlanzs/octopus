package task

import (
	"context"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/price"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/utils/log"
)

const (
	TaskStatsSave         = "stats_save"
	TaskRelayLogSave      = "relay_log_save"
	TaskBaseUrlDelay      = "base_url_delay"
	TaskWSAffinityCleanup = "ws_affinity_cleanup"
)

func Init() {
	priceUpdateIntervalHours, err := op.SettingGetInt(model.SettingKeyModelInfoUpdateInterval)
	if err != nil {
		log.Errorf("failed to get model info update interval: %v", err)
		return
	}
	priceUpdateInterval := time.Duration(priceUpdateIntervalHours) * time.Hour
	// 注册价格更新任务
	Register(string(model.SettingKeyModelInfoUpdateInterval), priceUpdateInterval, true, func() {
		if err := price.UpdateLLMPrice(context.Background()); err != nil {
			log.Warnf("failed to update price info: %v", err)
		}
	})

	// 注册基础URL延迟任务
	Register(TaskBaseUrlDelay, 24*time.Hour, true, ChannelBaseUrlDelayTask)

	// 注册LLM同步任务
	syncLLMIntervalHours, err := op.SettingGetInt(model.SettingKeySyncLLMInterval)
	if err != nil {
		log.Warnf("failed to get sync LLM interval: %v", err)
		return
	}
	syncLLMInterval := time.Duration(syncLLMIntervalHours) * time.Hour
	Register(string(model.SettingKeySyncLLMInterval), syncLLMInterval, true, SyncModelsTask)

	siteSyncIntervalHours, err := op.SettingGetInt(model.SettingKeySiteSyncInterval)
	if err != nil {
		log.Warnf("failed to get site sync interval: %v", err)
		return
	}
	siteSyncInterval := time.Duration(siteSyncIntervalHours) * time.Hour
	Register(string(model.SettingKeySiteSyncInterval), siteSyncInterval, true, SiteSyncTask)

	siteCheckinIntervalHours, err := op.SettingGetInt(model.SettingKeySiteCheckinInterval)
	if err != nil {
		log.Warnf("failed to get site checkin interval: %v", err)
		return
	}
	siteCheckinInterval := time.Duration(siteCheckinIntervalHours) * time.Hour
	Register(string(model.SettingKeySiteCheckinInterval), siteCheckinInterval, true, SiteCheckinTask)

	// 注册统计保存任务（顺带回收熔断器空闲条目，防止 (channel,key,model) 无界增长）
	statsSaveIntervalMinutes, err := op.SettingGetInt(model.SettingKeyStatsSaveInterval)
	if err != nil {
		log.Warnf("failed to get stats save interval: %v", err)
		return
	}
	statsSaveInterval := time.Duration(statsSaveIntervalMinutes) * time.Minute
	Register(TaskStatsSave, statsSaveInterval, false, func() {
		op.StatsSaveDBTask()
		if reaped := balancer.ReapBreakers(time.Now(), 30*time.Minute); reaped > 0 {
			log.Debugf("circuit breaker reaped %d idle entries", reaped)
		}
	})
	// 注册中继日志保存任务
	Register(TaskRelayLogSave, time.Hour, false, func() {
		if err := op.RelayLogSaveDBTask(context.Background()); err != nil {
			log.Warnf("relay log save db task failed: %v", err)
		}
	})

	Register(TaskWSAffinityCleanup, 10*time.Minute, false, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		deleted, err := op.WSResponseAffinityCleanup(ctx, time.Now())
		if err != nil {
			log.Warnf("ws response affinity cleanup failed: %v", err)
			return
		}
		if deleted > 0 {
			log.Debugf("ws response affinity cleanup removed %d expired rows", deleted)
		}
	})

	// 注册自动排序(Auto)任务（默认间隔 60 秒，总开关在任务内判断）
	autoRankIntervalSec, err := op.SettingGetInt(model.SettingKeyAutoRankInterval)
	if err != nil || autoRankIntervalSec <= 0 {
		autoRankIntervalSec = 60
	}
	Register(string(model.SettingKeyAutoRankInterval), time.Duration(autoRankIntervalSec)*time.Second, false, AutoRankTask)

	// 注册自动排序主动探测任务（默认间隔 300 秒，总开关与渠道开关在任务内判断）
	autoRankProbeIntervalSec, err := op.SettingGetInt(model.SettingKeyAutoRankProbeInterval)
	if err != nil || autoRankProbeIntervalSec <= 0 {
		autoRankProbeIntervalSec = 300
	}
	Register(string(model.SettingKeyAutoRankProbeInterval), time.Duration(autoRankProbeIntervalSec)*time.Second, false, AutoRankProbeTask)

	// 注册被动离群退役(POR)任务（默认间隔 2 分钟，总开关在任务内判断）
	outlierIntervalMinutes, err := op.SettingGetInt(model.SettingKeyOutlierRetireInterval)
	if err != nil || outlierIntervalMinutes <= 0 {
		outlierIntervalMinutes = 2
	}
	Register(string(model.SettingKeyOutlierRetireInterval), time.Duration(outlierIntervalMinutes)*time.Minute, false, SiteOutlierRetireTask)

	// 注册 WebDAV 自动备份任务（间隔为 0 时不运行）
	webdavIntervalHours, err := op.SettingGetInt(model.SettingKeyWebDAVBackupInterval)
	if err != nil {
		log.Warnf("failed to get webdav backup interval: %v", err)
	} else if webdavIntervalHours > 0 {
		webdavInterval := time.Duration(webdavIntervalHours) * time.Hour
		Register(string(model.SettingKeyWebDAVBackupInterval), webdavInterval, false, WebDAVBackupTask)
	}
}
