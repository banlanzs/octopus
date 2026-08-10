package balancer

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/utils/log"
)

// CircuitState 熔断器状态
type CircuitState int

type FailureKind int

const (
	StateClosed   CircuitState = iota // 正常通行
	StateOpen                         // 熔断中，拒绝所有请求
	StateHalfOpen                     // 半开，仅允许单个试探请求

	FailureHard FailureKind = iota
	FailureSoftRateLimit
)

// circuitEntry 单个熔断器条目
type circuitEntry struct {
	State               CircuitState
	ConsecutiveFailures int64
	LastFailureTime     time.Time
	TripCount           int // 累计熔断触发次数（用于指数退避）
	HalfOpenSince       time.Time
	lastSeen            time.Time // 最近一次读写时间，供内存回收（ReapBreakers）
	mu                  sync.Mutex
}

// 全局熔断器存储
var globalBreaker sync.Map // key: string -> value: *circuitEntry

// circuitKey 生成熔断器键：channelID:channelKeyID:modelName
func circuitKey(channelID, keyID int, modelName string) string {
	return fmt.Sprintf("%d:%d:%s", channelID, keyID, modelName)
}

// channelBreakerPrefix 渠道级熔断器 key 前缀（纯数字 key 与 channel:key:model 格式区分）。
const channelBreakerPrefix = "c:"

func channelCircuitKey(channelID int) string {
	return channelBreakerPrefix + strconv.Itoa(channelID)
}

func resetCircuitBreakerByChannel(channelID int) {
	prefix := fmt.Sprintf("%d:", channelID)
	globalBreaker.Range(func(key, _ any) bool {
		if k, ok := key.(string); ok && strings.HasPrefix(k, prefix) {
			globalBreaker.Delete(k)
		}
		return true
	})
	globalBreaker.Delete(channelCircuitKey(channelID))
}

// IsChannelLevelFailure 判断失败是否为渠道级信号（渠道服务/网络问题）。
// 排除 Key 级认证(401/402/403/405)、限流(429)、客户端噪音与配额类。
// 该判定用于渠道级冷却触发，与 AutoRank 可计数失败集合基本一致但不含 Key 级。
func IsChannelLevelFailure(statusCode int) bool {
	switch statusCode {
	case 0, 500, 502, 503, 504, 520, 521, 524, 597, 598, 599:
		return true
	default:
		return false
	}
}

// channelThreshold 渠道级熔断连续失败阈值（默认 3，渠道整体故障更快摘除）。
func channelThreshold() int64 {
	v, err := op.SettingGetInt(model.SettingKeyCircuitBreakerChannelThreshold)
	if err != nil || v <= 0 {
		return 3
	}
	return int64(v)
}

// IsChannelTripped 检查渠道级熔断状态（粒度 = 整个渠道，与模型/Key 无关）。
func IsChannelTripped(channelID int) (tripped bool, remaining time.Duration) {
	return isTrippedKey(channelCircuitKey(channelID))
}

// ChannelRecoveryIn 返回渠道级熔断的剩余冷却时间；0 表示未熔断/已恢复。
func ChannelRecoveryIn(channelID int) time.Duration {
	tripped, remaining := IsChannelTripped(channelID)
	if !tripped {
		return 0
	}
	return remaining
}

// ChannelCircuitStatus 只读返回渠道级熔断状态（供管理面板展示，不修改
// 状态机，也不会把 Open → HalfOpen 推进）：tripped 是否处于熔断且未过
// 冷却，remaining 剩余冷却时长（HalfOpen 返回 0），tripCount 累计熔断次数。
func ChannelCircuitStatus(channelID int) (tripped bool, remaining time.Duration, tripCount int) {
	v, ok := globalBreaker.Load(channelCircuitKey(channelID))
	if !ok {
		return false, 0, 0
	}
	entry := v.(*circuitEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	switch entry.State {
	case StateOpen:
		cooldown := GetCooldown(entry.TripCount)
		elapsed := time.Since(entry.LastFailureTime)
		if elapsed >= cooldown {
			return false, 0, entry.TripCount
		}
		return true, cooldown - elapsed, entry.TripCount
	case StateHalfOpen:
		return true, 0, entry.TripCount
	default:
		return false, 0, entry.TripCount
	}
}

// RecordChannelSuccess 渠道级成功：重置渠道熔断状态。
func RecordChannelSuccess(channelID int) {
	key := channelCircuitKey(channelID)
	v, ok := globalBreaker.Load(key)
	if !ok {
		return
	}
	entry := v.(*circuitEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.State = StateClosed
	entry.ConsecutiveFailures = 0
	entry.TripCount = 0
	entry.HalfOpenSince = time.Time{}
}

// RecordChannelFailure 渠道级失败：连续渠道级信号达到阈值后整渠道熔断
// （指数退避同 key 级）。SoftRateLimit（429/503 透传场景）不累计。
func RecordChannelFailure(channelID int, kind FailureKind) {
	key := channelCircuitKey(channelID)
	entry := getOrCreateEntry(key)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.LastFailureTime = time.Now()
	entry.HalfOpenSince = time.Time{}
	entry.lastSeen = time.Now()
	switch entry.State {
	case StateClosed:
		if kind == FailureSoftRateLimit {
			return
		}
		entry.ConsecutiveFailures++
		if entry.ConsecutiveFailures >= channelThreshold() {
			entry.State = StateOpen
			entry.TripCount++
			log.Warnf("channel circuit breaker [%s] Closed -> Open (failures=%d >= channel threshold=%d, tripCount=%d, cooldown=%v)",
				key, entry.ConsecutiveFailures, channelThreshold(), entry.TripCount, GetCooldown(entry.TripCount))
		}
	case StateHalfOpen:
		entry.State = StateOpen
		entry.TripCount++
		entry.ConsecutiveFailures = 0
	case StateOpen:
	}
}

// fallbackProbeMinInterval 全冷却兜底探测的最小间隔：全渠道故障风暴时，
// 限制对同一渠道的真实上游探测频率，避免把已挂供应商打爆。
const fallbackProbeMinInterval = 30 * time.Second

var probeGuard sync.Map // channelID -> int64 (unix nanos of last fallback probe)

// TryAcquireFallbackProbe 尝试获取一次 fallback 探测许可。
// 同一渠道在 fallbackProbeMinInterval 内仅放行一次；未受限时记录并返回 true。
func TryAcquireFallbackProbe(channelID int) bool {
	now := time.Now().UnixNano()
	interval := int64(fallbackProbeMinInterval)
	if v, ok := probeGuard.Load(channelID); ok {
		if now-v.(int64) < interval {
			return false
		}
	}
	probeGuard.Store(channelID, now)
	return true
}

// getOrCreateEntry 获取或创建熔断器条目
func getOrCreateEntry(key string) *circuitEntry {
	if v, ok := globalBreaker.Load(key); ok {
		return v.(*circuitEntry)
	}
	entry := &circuitEntry{State: StateClosed, lastSeen: time.Now()}
	actual, _ := globalBreaker.LoadOrStore(key, entry)
	return actual.(*circuitEntry)
}

// ReapBreakers 回收 lastSeen 早于 now-ttl 的熔断条目（已删除渠道/长期无流量）。
// 与 AutoRankReap 同构；由周期任务调用，防止 (channel,key,model) 与渠道级条目无限增长。
func ReapBreakers(now time.Time, ttl time.Duration) int {
	if ttl <= 0 {
		return 0
	}
	cutoff := now.Add(-ttl)
	reaped := 0
	globalBreaker.Range(func(key, value any) bool {
		e := value.(*circuitEntry)
		e.mu.Lock()
		if e.lastSeen.Before(cutoff) {
			globalBreaker.Delete(key)
			reaped++
		}
		e.mu.Unlock()
		return true
	})
	return reaped
}

// getThreshold 获取熔断阈值配置
func getThreshold() int64 {
	v, err := op.SettingGetInt(model.SettingKeyCircuitBreakerThreshold)
	if err != nil || v <= 0 {
		return 5
	}
	return int64(v)
}

// GetCooldown 获取当前冷却时间（带指数退避）
func GetCooldown(tripCount int) time.Duration {
	base, err := op.SettingGetInt(model.SettingKeyCircuitBreakerCooldown)
	if err != nil || base <= 0 {
		base = 60
	}
	maxCooldown, err := op.SettingGetInt(model.SettingKeyCircuitBreakerMaxCooldown)
	if err != nil || maxCooldown <= 0 {
		maxCooldown = 600
	}

	// 指数退避：baseCooldown * 2^(tripCount-1)
	cooldown := base
	if tripCount > 1 {
		shift := tripCount - 1
		if shift > 20 { // 防止溢出
			shift = 20
		}
		// 防御：base 过大时左移可能溢出为负（负冷却 = 立即重开探测）。
		// 先与 maxCooldown 对齐，超限值由下方统一截断。
		if base > math.MaxInt64>>uint(shift) {
			cooldown = math.MaxInt64
		} else {
			cooldown = base << shift
		}
	}
	if cooldown > maxCooldown {
		cooldown = maxCooldown
	}

	return time.Duration(cooldown) * time.Second
}

// IsTripped 检查通道（channel:key:model 粒度）是否处于熔断状态
// 返回 tripped=true 表示该通道应被跳过，remaining 为剩余冷却时间
func IsTripped(channelID, keyID int, modelName string) (tripped bool, remaining time.Duration) {
	return isTrippedKey(circuitKey(channelID, keyID, modelName))
}

func isTrippedKey(key string) (tripped bool, remaining time.Duration) {
	v, ok := globalBreaker.Load(key)
	if !ok {
		return false, 0 // 无记录，视为 Closed
	}
	entry := v.(*circuitEntry)

	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.lastSeen = time.Now()

	switch entry.State {
	case StateClosed:
		return false, 0

	case StateOpen:
		cooldown := GetCooldown(entry.TripCount)
		elapsed := time.Since(entry.LastFailureTime)
		if elapsed >= cooldown {
			now := time.Now()
			entry.State = StateHalfOpen
			entry.HalfOpenSince = now
			log.Infof("circuit breaker [%s] Open -> HalfOpen (cooldown %v elapsed)", key, cooldown)
			return false, 0
		}
		// 仍在冷却中
		return true, cooldown - elapsed

	case StateHalfOpen:
		cooldown := GetCooldown(entry.TripCount)
		if entry.HalfOpenSince.IsZero() {
			entry.HalfOpenSince = time.Now()
		}
		if time.Since(entry.HalfOpenSince) >= cooldown {
			entry.State = StateOpen
			entry.LastFailureTime = time.Now()
			entry.HalfOpenSince = time.Time{}
			log.Warnf("circuit breaker [%s] HalfOpen -> Open (probe timed out, cooldown=%v)", key, cooldown)
			return true, cooldown
		}
		// 已有试探请求在进行中，拒绝其他请求
		return true, 0

	default:
		return false, 0
	}
}

// RecordSuccess 记录成功，重置熔断器状态
func RecordSuccess(channelID, keyID int, modelName string) {
	key := circuitKey(channelID, keyID, modelName)
	v, ok := globalBreaker.Load(key)
	if !ok {
		return
	}
	entry := v.(*circuitEntry)

	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.lastSeen = time.Now()

	if entry.State == StateHalfOpen {
		log.Infof("circuit breaker [%s] HalfOpen -> Closed (probe succeeded)", key)
	}

	// 重置全部状态
	entry.State = StateClosed
	entry.ConsecutiveFailures = 0
	entry.TripCount = 0
	entry.HalfOpenSince = time.Time{}
}

// RecordFailure 记录失败，可能触发熔断。
// FailureSoftRateLimit 用于 429/503 这类软失败：Closed 状态下不累计阈值，
// HalfOpen 状态下重新进入 Open，但不放大 TripCount。
func RecordFailure(channelID, keyID int, modelName string, kind FailureKind) {
	key := circuitKey(channelID, keyID, modelName)
	entry := getOrCreateEntry(key)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	entry.LastFailureTime = time.Now()
	entry.HalfOpenSince = time.Time{}
	entry.lastSeen = time.Now()

	switch entry.State {
	case StateClosed:
		if kind == FailureSoftRateLimit {
			return
		}
		entry.ConsecutiveFailures++
		threshold := getThreshold()
		if entry.ConsecutiveFailures >= threshold {
			entry.State = StateOpen
			entry.TripCount++
			log.Warnf("circuit breaker [%s] Closed -> Open (failures=%d >= threshold=%d, tripCount=%d, cooldown=%v)",
				key, entry.ConsecutiveFailures, threshold, entry.TripCount, GetCooldown(entry.TripCount))
		}

	case StateHalfOpen:
		if kind == FailureSoftRateLimit {
			entry.State = StateOpen
			log.Warnf("circuit breaker [%s] HalfOpen -> Open (soft rate limit, tripCount=%d, cooldown=%v)",
				key, entry.TripCount, GetCooldown(entry.TripCount))
			return
		}
		// 试探失败，重新进入 Open 状态，TripCount 递增（冷却时间翻倍）
		entry.State = StateOpen
		entry.TripCount++
		entry.ConsecutiveFailures = 0 // 重新开始计数
		log.Warnf("circuit breaker [%s] HalfOpen -> Open (probe failed, tripCount=%d, cooldown=%v)",
			key, entry.TripCount, GetCooldown(entry.TripCount))

	case StateOpen:
		// 理论上不应该在 Open 状态下接收到失败记录（请求应被拒绝），
		// 但为安全起见仍更新失败时间
	}
}
