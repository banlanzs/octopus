package balancer

import (
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func TestRecordAutoSampleWindowStats(t *testing.T) {
	AutoRankReset()
	for i := 0; i < 3; i++ {
		RecordAutoSample(1, "gpt-4o", true, 500)
	}
	for i := 0; i < 2; i++ {
		RecordAutoSample(1, "gpt-4o", false, 0)
	}
	st := GetAutoRankStats(1, "gpt-4o")
	if st.Samples != 5 {
		t.Fatalf("expected 5 samples, got %d", st.Samples)
	}
	if st.Failures != 2 {
		t.Fatalf("expected 2 failures, got %d", st.Failures)
	}
	if st.SuccessRate < 0.59 || st.SuccessRate > 0.61 {
		t.Fatalf("expected success rate 0.6, got %v", st.SuccessRate)
	}
	if st.EWMALatencyMS != 500 {
		t.Fatalf("expected ewma latency 500, got %v", st.EWMALatencyMS)
	}
}

func TestRecordAutoSampleRingOverwrite(t *testing.T) {
	AutoRankReset()
	// 写满窗口（20 条失败），再写 1 条成功 → 最旧失败被覆盖
	for i := 0; i < AutoRankPhysicalCap; i++ {
		RecordAutoSample(2, "gpt-4o", false, 0)
	}
	RecordAutoSample(2, "gpt-4o", true, 100)
	st := GetAutoRankStats(2, "gpt-4o")
	if st.Samples != AutoRankPhysicalCap {
		t.Fatalf("expected %d samples, got %d", AutoRankPhysicalCap, st.Samples)
	}
	if st.Failures != AutoRankPhysicalCap-1 {
		t.Fatalf("expected %d failures, got %d", AutoRankPhysicalCap-1, st.Failures)
	}
}

func TestAutoRankStatsTimeWindowExpiry(t *testing.T) {
	AutoRankReset()
	e := getOrCreateAutoRank(autoRankKey(3, "gpt-4o"))
	e.mu.Lock()
	e.buf[0] = autoRankSample{at: time.Now().Add(-20 * time.Minute), success: true}
	e.buf[1] = autoRankSample{at: time.Now(), success: false}
	e.next = 2
	e.size = 2
	e.lastSeen = time.Now()
	e.mu.Unlock()

	st := GetAutoRankStats(3, "gpt-4o")
	if st.Samples != 1 {
		t.Fatalf("expected 1 valid sample after expiry, got %d", st.Samples)
	}
	if st.Failures != 1 {
		t.Fatalf("expected 1 failure, got %d", st.Failures)
	}
}

func TestScoreFromStats(t *testing.T) {
	fast := scoreFromStats(AutoRankStats{Samples: 10, SuccessRate: 1, EWMALatencyMS: 500})
	slow := scoreFromStats(AutoRankStats{Samples: 10, SuccessRate: 1, EWMALatencyMS: 3000})
	flaky := scoreFromStats(AutoRankStats{Samples: 10, SuccessRate: 0.5, EWMALatencyMS: 500})
	if fast <= slow {
		t.Fatalf("expected fast candidate to outrank slow: fast=%v slow=%v", fast, slow)
	}
	if flaky >= fast {
		t.Fatalf("expected flaky candidate to rank below stable: flaky=%v stable=%v", flaky, fast)
	}
}

func TestAutoRankLessTiers(t *testing.T) {
	no := AutoRankStats{}
	enough := AutoRankStats{Samples: 5, SuccessRate: 0.9, EWMALatencyMS: 1000}
	fast := AutoRankStats{Samples: 4, SuccessRate: 1, EWMALatencyMS: 300}

	if autoRankLess(no, enough) {
		t.Fatal("expected no-sample candidate to rank after enough-sample candidate")
	}
	if !autoRankLess(enough, no) {
		t.Fatal("expected enough-sample candidate to rank before no-sample candidate")
	}
	if !autoRankLess(fast, enough) {
		t.Fatal("expected faster candidate to rank before slower one")
	}
	if autoRankLess(enough, fast) {
		t.Fatal("expected slower candidate not to rank before faster one")
	}
	if autoRankLess(fast, AutoRankStats{Samples: 4, SuccessRate: 1, EWMALatencyMS: 300}) {
		t.Fatal("expected equal candidates to keep original order")
	}
}

func TestFindUnderSampled(t *testing.T) {
	AutoRankReset()
	// c1 样本充足；c2 无样本（最需探索）；c3 样本不足
	for i := 0; i < 5; i++ {
		RecordAutoSample(1, "gpt-4o", true, 100)
	}
	RecordAutoSample(2, "gpt-4o", true, 100)
	RecordAutoSample(3, "gpt-4o", true, 100)
	RecordAutoSample(3, "gpt-4o", true, 100)

	items := []model.GroupItem{
		{ChannelID: 1, ModelName: "gpt-4o"},
		{ChannelID: 2, ModelName: "gpt-4o"},
		{ChannelID: 3, ModelName: "gpt-4o"},
	}
	if idx := findUnderSampled(items, 3); idx != 1 {
		t.Fatalf("expected under-sampled index 1, got %d", idx)
	}

	AutoRankReset()
	for i := 0; i < 5; i++ {
		RecordAutoSample(4, "gpt-4o", true, 100)
	}
	items2 := []model.GroupItem{{ChannelID: 4, ModelName: "gpt-4o"}}
	if idx := findUnderSampled(items2, 3); idx != -1 {
		t.Fatalf("expected no under-sampled candidate, got %d", idx)
	}
}

func TestAutoRankRestore(t *testing.T) {
	AutoRankReset()
	AutoRankRestore([]model.AutoRankSnapshot{
		{ChannelID: 6, ModelName: "gpt-4o", Samples: 10, Failures: 2, EWMALatencyMS: 800},
	})
	st := GetAutoRankStats(6, "gpt-4o")
	if st.Samples != 10 {
		t.Fatalf("expected 10 restored samples, got %d", st.Samples)
	}
	if st.Failures != 2 {
		t.Fatalf("expected 2 restored failures, got %d", st.Failures)
	}
	if st.EWMALatencyMS != 800 {
		t.Fatalf("expected restored ewma 800, got %v", st.EWMALatencyMS)
	}
}

func TestAutoRankAllStatsSkipsEmpty(t *testing.T) {
	AutoRankReset()
	RecordAutoSample(7, "gpt-4o", true, 100)
	_ = getOrCreateAutoRank(autoRankKey(8, "gpt-4o"))
	all := AutoRankAllStats()
	if len(all) != 1 {
		t.Fatalf("expected 1 stats entry, got %d", len(all))
	}
	if all[0].ChannelID != 7 || all[0].ModelName != "gpt-4o" {
		t.Fatalf("unexpected stats entry: %+v", all[0])
	}
}
