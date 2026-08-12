package balancer

import (
	"testing"
	"time"
)

// TestRecordQualityFailureTripsAndRecovers 验证质量失败：置 Open → 冷却期内
// IsTripped=true → 冷却结束后自动 HalfOpen（不再跳过）→ 成功后恢复 Closed。
func TestRecordQualityFailureTripsAndRecovers(t *testing.T) {
	const (
		channelID = 9001
		keyID     = 9002
		model     = "deepseek-v4-flash"
	)
	// 清理可能的历史状态
	globalBreaker.Delete(circuitKey(channelID, keyID, model))

	// 质量失败：冷却 50ms（测试用短时长）
	RecordQualityFailure(channelID, keyID, model, 50*time.Millisecond)

	// 冷却期内应被跳过
	if tripped, remaining := IsTripped(channelID, keyID, model); !tripped {
		t.Fatalf("质量失败后应立即 Open（tripped=false, remaining=%v）", remaining)
	}

	// 等待冷却结束 → HalfOpen，不再跳过
	time.Sleep(80 * time.Millisecond)
	if tripped, _ := IsTripped(channelID, keyID, model); tripped {
		t.Fatalf("冷却结束后应进入 HalfOpen 探测（仍 tripped）")
	}

	// 探测成功 → Closed
	RecordSuccess(channelID, keyID, model)
	if tripped, _ := IsTripped(channelID, keyID, model); tripped {
		t.Fatalf("HalfOpen 探测成功后应恢复 Closed")
	}
}

// TestRecordQualityFailureDoesNotEscalateTripCount 验证质量失败不触发指数退避
// （TripCount 不递增）：两次质量失败的冷却时长一致。
func TestRecordQualityFailureDoesNotEscalateTripCount(t *testing.T) {
	const (
		channelID = 9003
		keyID     = 9004
		model     = "deepseek-v4-flash"
	)
	globalBreaker.Delete(circuitKey(channelID, keyID, model))

	RecordQualityFailure(channelID, keyID, model, 30*time.Millisecond)
	// 冷却结束后进入 HalfOpen
	time.Sleep(60 * time.Millisecond)
	IsTripped(channelID, keyID, model) // 触发 HalfOpen 转换

	// 第二次质量失败（HalfOpen 状态下）→ 仍为固定冷却，TripCount 不应递增
	RecordQualityFailure(channelID, keyID, model, 30*time.Millisecond)
	entry, _ := globalBreaker.Load(circuitKey(channelID, keyID, model))
	e := entry.(*circuitEntry)
	e.mu.Lock()
	trip := e.TripCount
	e.mu.Unlock()
	if trip != 0 {
		t.Fatalf("质量失败不应递增 TripCount（got %d, want 0）", trip)
	}
}
