package balancer

import (
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func TestResetCircuitBreakerByChannelRemovesOnlyTargetChannel(t *testing.T) {
	Reset()
	globalBreaker.Store(circuitKey(1, 10, "gpt-4o"), &circuitEntry{
		State:           StateOpen,
		LastFailureTime: time.Now(),
		TripCount:       1,
	})
	globalBreaker.Store(circuitKey(10, 10, "gpt-4o"), &circuitEntry{
		State:           StateOpen,
		LastFailureTime: time.Now(),
		TripCount:       1,
	})
	globalBreaker.Store(circuitKey(2, 20, "gpt-4o"), &circuitEntry{
		State:           StateOpen,
		LastFailureTime: time.Now(),
		TripCount:       1,
	})

	ResetStateByChannel(1)

	if tripped, _ := IsTripped(1, 10, "gpt-4o"); tripped {
		t.Fatal("expected target channel circuit breaker to be reset")
	}
	if tripped, _ := IsTripped(10, 10, "gpt-4o"); !tripped {
		t.Fatal("expected channel with similar prefix to remain tripped")
	}
	if tripped, _ := IsTripped(2, 20, "gpt-4o"); !tripped {
		t.Fatal("expected unrelated channel circuit breaker to remain tripped")
	}
}

// 渠道级失败信号分类：渠道服务/网络问题计入，Key 级/限流/客户端噪音不计
func TestIsChannelLevelFailure(t *testing.T) {
	channelLevel := []int{0, 500, 502, 503, 504, 520, 521, 524, 597, 598, 599}
	notChannel := []int{400, 401, 402, 403, 404, 405, 408, 415, 422, 429, 499, 596}
	for _, code := range channelLevel {
		if !IsChannelLevelFailure(code) {
			t.Fatalf("status %d should be channel-level failure", code)
		}
	}
	for _, code := range notChannel {
		if IsChannelLevelFailure(code) {
			t.Fatalf("status %d should NOT be channel-level failure (key-level/ratelimit/client noise)", code)
		}
	}
}

// 渠道级熔断状态机：连续渠道级失败触发 → 整渠道熔断 → 成功重置 → HalfOpen 探测
func TestChannelCircuitBreaker(t *testing.T) {
	Reset()
	// 连续 3 次渠道级失败（channelThreshold 默认 3）触发熔断
	RecordChannelFailure(1, FailureHard)
	RecordChannelFailure(1, FailureHard)
	if tripped, _ := IsChannelTripped(1); tripped {
		t.Fatal("expected channel 1 still closed after 2 failures (threshold 3)")
	}
	RecordChannelFailure(1, FailureHard)
	if tripped, _ := IsChannelTripped(1); !tripped {
		t.Fatal("expected channel 1 tripped after 3 channel-level failures")
	}

	// 成功重置渠道熔断
	RecordChannelSuccess(1)
	if tripped, _ := IsChannelTripped(1); tripped {
		t.Fatal("expected channel 1 closed after success")
	}

	// Soft（429 透传场景）在 Closed 下不累计渠道失败
	RecordChannelFailure(2, FailureSoftRateLimit)
	RecordChannelFailure(2, FailureSoftRateLimit)
	RecordChannelFailure(2, FailureSoftRateLimit)
	if tripped, _ := IsChannelTripped(2); tripped {
		t.Fatal("expected soft failures not to accumulate channel circuit breaker in Closed state")
	}

	// key 级熔断与渠道级熔断相互独立
	Reset()
	RecordFailure(1, 10, "gpt-4o", FailureHard)
	RecordFailure(1, 10, "gpt-4o", FailureHard)
	if tripped, _ := IsChannelTripped(1); tripped {
		t.Fatal("expected channel-level breaker independent from key-level breaker")
	}
	if tripped, _ := IsTripped(1, 10, "gpt-4o"); tripped {
		t.Fatal("expected key-level breaker still closed at 2 failures")
	}
}

// 全冷却兜底：选渠道级熔断中最早恢复的候选
func TestEarliestRecoveryChannel(t *testing.T) {
	Reset()
	items := []model.GroupItem{
		{ChannelID: 1, ModelName: "m1"},
		{ChannelID: 2, ModelName: "m2"},
		{ChannelID: 3, ModelName: "m3"},
	}
	// 无熔断候选 → 不兜底
	if _, ok := EarliestRecoveryChannel(items); ok {
		t.Fatal("expected no fallback candidate when nothing is tripped")
	}

	// 渠道 1 熔断 2s、渠道 2 熔断 30s、渠道 3 正常
	entry1 := getOrCreateEntry(channelCircuitKey(1))
	entry1.mu.Lock()
	entry1.State = StateOpen
	entry1.LastFailureTime = time.Now()
	entry1.TripCount = 1
	entry1.mu.Unlock()
	entry2 := getOrCreateEntry(channelCircuitKey(2))
	entry2.mu.Lock()
	entry2.State = StateOpen
	entry2.LastFailureTime = time.Now()
	entry2.TripCount = 3 // tripCount 更高 → 退避更长
	entry2.mu.Unlock()

	best, ok := EarliestRecoveryChannel(items)
	if !ok {
		t.Fatal("expected fallback candidate when channels are tripped")
	}
	if best.ChannelID != 1 {
		t.Fatalf("expected earliest recovery channel 1, got %d", best.ChannelID)
	}
}

// fallback 探测节流：同一渠道在最小间隔内只放行一次
func TestTryAcquireFallbackProbe(t *testing.T) {
	Reset()
	// 首次获取成功
	if !TryAcquireFallbackProbe(1) {
		t.Fatal("expected first probe acquisition to succeed")
	}
	// 间隔内再次获取失败（节流）
	if TryAcquireFallbackProbe(1) {
		t.Fatal("expected probe to be throttled within min interval")
	}
	// 不同渠道不受影响
	if !TryAcquireFallbackProbe(2) {
		t.Fatal("expected different channel probe to succeed")
	}
	// 手动把 lastProbe 拨回过去 → 可再次获取
	probeGuard.Store(1, time.Now().Add(-2*fallbackProbeMinInterval).UnixNano())
	if !TryAcquireFallbackProbe(1) {
		t.Fatal("expected probe to succeed after interval elapsed")
	}
}

// 熔断条目回收：lastSeen 超时的条目被删除，活跃条目保留
func TestReapBreakers(t *testing.T) {
	Reset()
	RecordFailure(1, 10, "gpt-4o", FailureHard)
	// 手动把条目 lastSeen 拨回过去（模拟长期无流量）
	if v, ok := globalBreaker.Load(circuitKey(1, 10, "gpt-4o")); ok {
		e := v.(*circuitEntry)
		e.mu.Lock()
		e.lastSeen = time.Now().Add(-time.Hour)
		e.mu.Unlock()
	}
	RecordFailure(2, 20, "gpt-4o", FailureHard) // 活跃条目（lastSeen=now）
	reaped := ReapBreakers(time.Now(), 30*time.Minute)
	if reaped != 1 {
		t.Fatalf("expected 1 reaped breaker entry, got %d", reaped)
	}
	if _, ok := globalBreaker.Load(circuitKey(1, 10, "gpt-4o")); ok {
		t.Fatal("expected idle breaker entry to be reaped")
	}
	if _, ok := globalBreaker.Load(circuitKey(2, 20, "gpt-4o")); !ok {
		t.Fatal("expected active breaker entry to survive reap")
	}
	// 渠道级条目同样回收
	RecordChannelFailure(3, FailureHard)
	if v, ok := globalBreaker.Load(channelCircuitKey(3)); ok {
		e := v.(*circuitEntry)
		e.mu.Lock()
		e.lastSeen = time.Now().Add(-time.Hour)
		e.mu.Unlock()
	}
	if reaped := ReapBreakers(time.Now(), 30*time.Minute); reaped != 1 {
		t.Fatalf("expected channel breaker entry reaped, got %d", reaped)
	}
}

func TestResetStickyByChannelRemovesOnlyTargetChannel(t *testing.T) {
	Reset()
	SetSticky(1, "gpt-4o", 10, 100)
	SetSticky(2, "gpt-4o", 20, 200)
	SetSticky(3, "claude", 10, 300)

	ResetStateByChannel(10)

	if entry := GetSticky(1, "gpt-4o", time.Minute); entry != nil {
		t.Fatalf("expected target channel sticky session to be reset, got %#v", entry)
	}
	if entry := GetSticky(3, "claude", time.Minute); entry != nil {
		t.Fatalf("expected second target channel sticky session to be reset, got %#v", entry)
	}
	if entry := GetSticky(2, "gpt-4o", time.Minute); entry == nil || entry.ChannelID != 20 {
		t.Fatalf("expected unrelated sticky session to remain, got %#v", entry)
	}
}

func TestHalfOpenDoesNotRemainTrippedForeverWithoutResult(t *testing.T) {
	Reset()
	key := circuitKey(7, 8, "gpt-4o")
	globalBreaker.Store(key, &circuitEntry{
		State:         StateHalfOpen,
		TripCount:     1,
		HalfOpenSince: time.Now().Add(-61 * time.Second),
	})

	tripped, remaining := IsTripped(7, 8, "gpt-4o")
	if !tripped {
		t.Fatal("expected expired half-open probe to be tripped again")
	}
	if remaining <= 0 {
		t.Fatalf("expected expired half-open probe to return cooldown, got %v", remaining)
	}

	value, ok := globalBreaker.Load(key)
	if !ok {
		t.Fatal("expected circuit entry to remain after half-open timeout")
	}
	entry := value.(*circuitEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.State != StateOpen {
		t.Fatalf("expected expired half-open entry to return to open, got %v", entry.State)
	}
	if !entry.HalfOpenSince.IsZero() {
		t.Fatalf("expected half-open timestamp to be cleared, got %v", entry.HalfOpenSince)
	}
}
