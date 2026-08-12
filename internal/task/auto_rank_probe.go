package task

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/grouphealth"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/utils/log"
)

// autoRankProbeConcurrency 单轮探测的并发上限，取与分组健康检查一致的保守值。
const autoRankProbeConcurrency = 2

// autoRankProbeTimeout 单轮任务的总时限。maxPerRound 默认 10、并发 2、单次探测
// 12s，最坏 5 批约 60s，留一倍余量。
const autoRankProbeTimeout = 2 * time.Minute

type autoRankProbeKey struct {
	groupID   int
	channelID int
	modelName string
}

// autoRankProbeLastAt 记录每个候选上次探测的时间，实现同候选探测冷却。
// 纯内存：重启后冷却重置（最多多发一轮探测）；候选从分组移除后由每轮的
// 存活集合回收，不会无界增长。
var autoRankProbeLastAt sync.Map // autoRankProbeKey -> time.Time

type autoRankProbeCandidate struct {
	key         autoRankProbeKey
	channel     model.Channel
	usedKey     model.ChannelKey
	realSamples int
	lastProbeAt time.Time
}

// AutoRankProbeTask 自动排序(Auto)主动探测任务：为欠采样候选补充成功率样本。
//
// 被动学习的盲区：零流量或低流量的候选，窗口样本会随 AutoRankTimeWindow 过期
// 清空，排序只能按冷启动兜底处理——已经挂掉的渠道和健康的渠道看起来一样，
// 直到真实用户请求撞上去才暴露。本任务对这类候选发起极小探测请求补齐信号。
//
// 探测结果只计成功率，不写延迟 EWMA、不推进"样本充足"判定
// （见 balancer.RecordAutoProbeSampleForGroup 与 AutoRankStats.RealSamples）：
// max_tokens=1 的探测耗时与真实转发不可比，若参与延迟维度会让被探测候选虚高。
//
// 风控约束——中转站与个人站点常拒绝无业务意图的测活请求，故三重收敛：
//   - 全局开关 auto_rank_probe_enabled 默认关闭；
//   - 渠道开关 Channel.ProbeEnabled 默认关闭，须逐个显式开启；
//   - 单轮探测数上限与同候选探测冷却均可配置。
func AutoRankProbeTask() {
	if enabled, err := op.SettingGetBool(model.SettingKeyAutoRankEnabled); err != nil || !enabled {
		return
	}
	if enabled, err := op.SettingGetBool(model.SettingKeyAutoRankProbeEnabled); err != nil || !enabled {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), autoRankProbeTimeout)
	defer cancel()

	candidates, alive := collectAutoRankProbeCandidates(ctx)
	reapAutoRankProbeState(alive)
	if len(candidates) == 0 {
		return
	}

	// 欠采样最严重的优先；同样欠采样时最久没探测过的优先，保证轮转覆盖。
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].realSamples != candidates[j].realSamples {
			return candidates[i].realSamples < candidates[j].realSamples
		}
		return candidates[i].lastProbeAt.Before(candidates[j].lastProbeAt)
	})
	if maxPerRound := autoRankProbeIntSetting(model.SettingKeyAutoRankProbeMaxPerRound, 10); len(candidates) > maxPerRound {
		candidates = candidates[:maxPerRound]
	}

	var (
		mu sync.Mutex
		ok int
		// failed 上游确实不可用（连接失败 / 认证 / 5xx），计入健康窗口。
		failed int
		// ignored 上游明确拒绝这类请求（400/404/429 等），说明站点不接受无业务
		// 意图的测活，不代表渠道不健康，不计入健康窗口。
		ignored int
		// aborted 任务自身超时或被取消，结果不可信，同样不计入。
		aborted int
		wg      sync.WaitGroup
	)
	prober := grouphealth.NewProber()
	sem := make(chan struct{}, autoRankProbeConcurrency)
	for _, candidate := range candidates {
		wg.Add(1)
		go func(c autoRankProbeCandidate) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result := prober.RunCandidate(ctx, c.channel, c.usedKey, c.key.modelName)
			autoRankProbeLastAt.Store(c.key, time.Now())

			// 任务级 ctx 超时/取消：失败来自我们自己而非上游，记进窗口就是冤枉渠道。
			if ctx.Err() != nil {
				mu.Lock()
				aborted++
				mu.Unlock()
				return
			}
			if !result.Success && !balancer.CountableFailure(result.HTTPStatus) {
				// 这类渠道应由用户关掉 probe_enabled，而不是让它在健康度上背锅。
				mu.Lock()
				ignored++
				mu.Unlock()
				return
			}

			balancer.RecordAutoProbeSampleForGroup(c.key.groupID, c.key.channelID, c.key.modelName, result.Success)
			mu.Lock()
			if result.Success {
				ok++
			} else {
				failed++
			}
			mu.Unlock()
			if !result.Success {
				// 不记响应体：可能含上游返回的敏感信息，状态码足够定位问题。
				log.Debugf("auto rank probe failed: channel=%d model=%s status=%d", c.key.channelID, c.key.modelName, result.HTTPStatus)
			}
		}(candidate)
	}
	wg.Wait()

	log.Debugf("auto rank probe: %d probed, %d ok, %d failed, %d ignored, %d aborted", len(candidates), ok, failed, ignored, aborted)
}

// collectAutoRankProbeCandidates 收集本轮可探测的候选，同时返回所有 Auto 分组
// 候选的存活集合（无论是否入选），供冷却状态回收使用。
// GroupList 失败时返回 nil 存活集合，表示"本轮无法判定存活"，不做回收。
func collectAutoRankProbeCandidates(ctx context.Context) ([]autoRankProbeCandidate, map[autoRankProbeKey]struct{}) {
	groups, err := op.GroupList(ctx)
	if err != nil {
		log.Warnf("auto rank probe list groups failed: %v", err)
		return nil, nil
	}

	minSamples := autoRankProbeIntSetting(model.SettingKeyAutoRankMinSamples, 3)
	cooldown := time.Duration(autoRankProbeIntSetting(model.SettingKeyAutoRankProbeCooldown, 600)) * time.Second
	now := time.Now()

	alive := make(map[autoRankProbeKey]struct{})
	channels := make(map[int]*model.Channel)
	var out []autoRankProbeCandidate

	for _, group := range groups {
		if group.Mode != model.GroupModeAuto {
			continue
		}
		for _, item := range group.Items {
			key := autoRankProbeKey{groupID: group.ID, channelID: item.ChannelID, modelName: item.ModelName}
			alive[key] = struct{}{}

			// 真实流量样本已足够，不必打扰上游。
			stats := balancer.GetAutoRankStatsForGroup(group.ID, item.ChannelID, item.ModelName)
			if stats.RealSamples() >= minSamples {
				continue
			}
			lastProbeAt := autoRankProbeLastSeen(key)
			if !lastProbeAt.IsZero() && now.Sub(lastProbeAt) < cooldown {
				continue
			}
			// 渠道级熔断中：熔断自带半开恢复机制，此时探测既会被浪费也会加剧风控。
			if tripped, _, _ := balancer.ChannelCircuitStatus(item.ChannelID); tripped {
				continue
			}

			channel, cached := channels[item.ChannelID]
			if !cached {
				loaded, err := op.ChannelGet(item.ChannelID, ctx)
				if err != nil {
					loaded = nil
				}
				channel = loaded
				channels[item.ChannelID] = loaded
			}
			if channel == nil || !channel.Enabled || !channel.ProbeEnabled {
				continue
			}
			usedKey := channel.GetChannelKey()
			if usedKey.ID == 0 || strings.TrimSpace(usedKey.ChannelKey) == "" {
				continue
			}

			out = append(out, autoRankProbeCandidate{
				key:         key,
				channel:     *channel,
				usedKey:     usedKey,
				realSamples: stats.RealSamples(),
				lastProbeAt: lastProbeAt,
			})
		}
	}
	return out, alive
}

func autoRankProbeLastSeen(key autoRankProbeKey) time.Time {
	if v, ok := autoRankProbeLastAt.Load(key); ok {
		if at, ok := v.(time.Time); ok {
			return at
		}
	}
	return time.Time{}
}

// reapAutoRankProbeState 清理已不在任何 Auto 分组中的候选冷却记录。
func reapAutoRankProbeState(alive map[autoRankProbeKey]struct{}) {
	if alive == nil {
		return
	}
	autoRankProbeLastAt.Range(func(k, _ any) bool {
		key, ok := k.(autoRankProbeKey)
		if !ok {
			autoRankProbeLastAt.Delete(k)
			return true
		}
		if _, live := alive[key]; !live {
			autoRankProbeLastAt.Delete(key)
		}
		return true
	})
}

func autoRankProbeIntSetting(key model.SettingKey, fallback int) int {
	v, err := op.SettingGetInt(key)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}
