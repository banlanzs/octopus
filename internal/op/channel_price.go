package op

import (
	"context"
	"fmt"
	"strings"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
	"gorm.io/gorm/clause"
)

// channelModelPriceCache 渠道内模型价格缓存。key 形如 "channelID:lower(modelName)"。
var channelModelPriceCache = cache.New[string, model.LLMPrice](16)

// channelModelPriceKey 构造缓存与唯一键用的复合键。
func channelModelPriceKey(channelID int, modelName string) string {
	return fmt.Sprintf("%d:%s", channelID, strings.ToLower(modelName))
}

// ChannelModelPriceGet 取渠道内模型价格；未配置时返回 (zero, error)，
// 由调用方决定是否回退到全局价。
func ChannelModelPriceGet(channelID int, modelName string) (model.LLMPrice, error) {
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	if channelID <= 0 || modelName == "" {
		return model.LLMPrice{}, fmt.Errorf("invalid channel model key")
	}
	price, ok := channelModelPriceCache.Get(channelModelPriceKey(channelID, modelName))
	if !ok {
		return model.LLMPrice{}, fmt.Errorf("channel model price not found")
	}
	return price, nil
}

// ChannelModelPriceUpsert 新增或更新渠道内模型价格，同步缓存。
func ChannelModelPriceUpsert(channelID int, modelName string, price model.LLMPrice, ctx context.Context) error {
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	if channelID <= 0 || modelName == "" {
		return fmt.Errorf("invalid channel model key")
	}
	row := model.ChannelModelPrice{
		ChannelID: channelID,
		ModelName: modelName,
		LLMPrice:  price,
	}
	if err := db.GetDB().WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_id"}, {Name: "model_name"}},
		DoUpdates: clause.AssignmentColumns([]string{"input", "output", "cache_read", "cache_write"}),
	}).Create(&row).Error; err != nil {
		return err
	}
	channelModelPriceCache.Set(channelModelPriceKey(channelID, modelName), price)
	return nil
}

// ChannelModelPriceDelete 删除渠道内模型价格（删除后回退全局价），同步缓存。
func ChannelModelPriceDelete(channelID int, modelName string, ctx context.Context) error {
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	if channelID <= 0 || modelName == "" {
		return fmt.Errorf("invalid channel model key")
	}
	if err := db.GetDB().WithContext(ctx).
		Where("channel_id = ? AND model_name = ?", channelID, modelName).
		Delete(&model.ChannelModelPrice{}).Error; err != nil {
		return err
	}
	channelModelPriceCache.Del(channelModelPriceKey(channelID, modelName))
	return nil
}

// ChannelModelPriceSeedDefaults 为渠道内尚未配置价格的模型批量写入默认价。
// prices[i] 与 modelNames[i] 一一对应（调用方算好默认值，避免 op 反向依赖 price 包）。
// 已存在的价格不覆盖。
func ChannelModelPriceSeedDefaults(channelID int, modelNames []string, prices []model.LLMPrice, ctx context.Context) error {
	if channelID <= 0 || len(modelNames) == 0 {
		return nil
	}
	n := len(modelNames)
	if len(prices) < n {
		n = len(prices)
	}
	rows := make([]model.ChannelModelPrice, 0, n)
	for i := 0; i < n; i++ {
		name := strings.ToLower(strings.TrimSpace(modelNames[i]))
		if name == "" {
			continue
		}
		if _, ok := channelModelPriceCache.Get(channelModelPriceKey(channelID, name)); ok {
			continue
		}
		rows = append(rows, model.ChannelModelPrice{
			ChannelID: channelID,
			ModelName: name,
			LLMPrice:  prices[i],
		})
	}
	if len(rows) == 0 {
		return nil
	}
	if err := db.GetDB().WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		channelModelPriceCache.Set(channelModelPriceKey(r.ChannelID, r.ModelName), r.LLMPrice)
	}
	return nil
}

// channelModelPriceRefreshCache 启动时从 DB 重建渠道内模型价格缓存。
// 表尚未创建（如部分测试环境）时视为空缓存，不阻塞缓存初始化。
func channelModelPriceRefreshCache(ctx context.Context) error {
	if !db.GetDB().Migrator().HasTable(&model.ChannelModelPrice{}) {
		return nil
	}
	rows := []model.ChannelModelPrice{}
	if err := db.GetDB().WithContext(ctx).Find(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		channelModelPriceCache.Set(channelModelPriceKey(r.ChannelID, r.ModelName), r.LLMPrice)
	}
	return nil
}
