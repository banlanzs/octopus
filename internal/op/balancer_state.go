package op

// resetRelayBalancerStateForChannel 渠道级运行时状态重置回调列表：
// 熔断器（balancer）、会话粘性（balancer）、协议能力学习缓存（relay）等
// 进程内状态在渠道更新/删除/分组变更时统一重置。支持多次注册。
var resetRelayBalancerStateForChannel []func(int)

func RegisterRelayBalancerStateReset(fn func(int)) {
	if fn == nil {
		return
	}
	resetRelayBalancerStateForChannel = append(resetRelayBalancerStateForChannel, fn)
}

func resetBalancerStateForChannel(channelID int) {
	for _, fn := range resetRelayBalancerStateForChannel {
		fn(channelID)
	}
}

func resetBalancerStateForChannels(channelIDs ...int) {
	if len(resetRelayBalancerStateForChannel) == 0 || len(channelIDs) == 0 {
		return
	}
	seen := make(map[int]struct{}, len(channelIDs))
	for _, channelID := range channelIDs {
		if channelID == 0 {
			continue
		}
		if _, ok := seen[channelID]; ok {
			continue
		}
		seen[channelID] = struct{}{}
		resetBalancerStateForChannel(channelID)
	}
}
