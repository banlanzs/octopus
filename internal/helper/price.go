package helper

import (
	"context"
	"strings"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/price"
)

func LLMPriceAddToDB(modelNames []string, ctx context.Context) error {
	newLLMInfos := make([]model.LLMInfo, 0, len(modelNames))
	for _, modelName := range modelNames {
		if modelName == "" {
			continue
		}
		modelPrice := price.GetLLMPrice(modelName)
		if modelPrice != nil {
			newLLMInfos = append(newLLMInfos, model.LLMInfo{
				Name:     modelName,
				LLMPrice: *modelPrice,
			})
		} else {
			newLLMInfos = append(newLLMInfos, model.LLMInfo{Name: modelName})
		}
	}
	if len(newLLMInfos) > 0 {
		return op.LLMBatchCreate(newLLMInfos, ctx)
	}
	return nil
}

// EnsureChannelModelPrices 为渠道内尚未配置价格的模型写入全局默认价。
// 已配置过（含用户改过价）的 (channel, model) 不覆盖。
func EnsureChannelModelPrices(channelID int, modelNames []string, ctx context.Context) error {
	if channelID <= 0 || len(modelNames) == 0 {
		return nil
	}
	names := make([]string, 0, len(modelNames))
	seen := make(map[string]struct{}, len(modelNames))
	prices := make([]model.LLMPrice, 0, len(modelNames))
	for _, modelName := range modelNames {
		modelName = strings.ToLower(strings.TrimSpace(modelName))
		if modelName == "" {
			continue
		}
		if _, ok := seen[modelName]; ok {
			continue
		}
		seen[modelName] = struct{}{}
		names = append(names, modelName)
		modelPrice := price.GetLLMPrice(modelName)
		if modelPrice == nil {
			modelPrice = &model.LLMPrice{}
		}
		prices = append(prices, *modelPrice)
	}
	if len(names) == 0 {
		return nil
	}
	return op.ChannelModelPriceSeedDefaults(channelID, names, prices, ctx)
}

func LLMPriceDeleteFromDBWithNoPrice(modelNames []string, ctx context.Context) error {
	if len(modelNames) == 0 {
		return nil
	}
	needDeleteModelNames := make([]string, 0, len(modelNames))
	for _, modelName := range modelNames {
		if modelName == "" {
			continue
		}
		modelPrice, err := op.LLMGet(modelName)
		if err != nil {
			return err
		}
		if modelPrice.Input != 0 || modelPrice.Output != 0 || modelPrice.CacheRead != 0 || modelPrice.CacheWrite != 0 {
			continue
		}
		needDeleteModelNames = append(needDeleteModelNames, modelName)
	}
	if len(needDeleteModelNames) > 0 {
		return op.LLMBatchDelete(needDeleteModelNames, ctx)
	}
	return nil
}
