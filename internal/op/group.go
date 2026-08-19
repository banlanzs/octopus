package op

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/cache"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var groupCache = cache.New[int, model.Group](16)
var groupMap = cache.New[string, model.Group](16)

func GroupList(ctx context.Context) ([]model.Group, error) {
	groups := make([]model.Group, 0, groupCache.Len())
	for _, group := range groupCache.GetAll() {
		groups = append(groups, group)
	}
	return groups, nil
}

func GroupListModel(ctx context.Context) ([]string, error) {
	models := []string{}
	for _, group := range groupCache.GetAll() {
		name := strings.TrimSpace(group.Name)
		if name == "" {
			continue
		}
		// 空分组保持原有语义（仍暴露分组名）。
		if len(group.Items) == 0 {
			models = append(models, name)
			continue
		}
		// 分组内所有条目都被渠道"仅暴露别名"隐藏时，不再暴露该分组名，
		// 与请求路由的过滤口径保持一致。
		visible := false
		for _, item := range group.Items {
			channel, ok := channelCache.Get(item.ChannelID)
			// 渠道缺失/禁用时保持旧语义：分组名仍照常暴露。
			if !ok || !channel.Enabled {
				visible = true
				break
			}
			if channel.ModelRedirectOnly && !channel.IsModelExposed(item.ModelName) {
				continue
			}
			visible = true
			break
		}
		if visible {
			models = append(models, name)
		}
	}
	return models, nil
}

// GroupListModelByChannelIDs 返回渠道白名单内可用的模型名。
// 同时覆盖两种来源：
//  1. 分组条目：分组通过 GroupItem 路由到白名单渠道（支持别名分组）；
//  2. 渠道模型配置：渠道直接暴露的模型（含模型重定向别名）。
func GroupListModelByChannelIDs(channelIDs map[int]struct{}, ctx context.Context) ([]string, error) {
	modelSet := make(map[string]struct{})

	// 来源 1：已有分组路由条目
	for _, group := range groupCache.GetAll() {
		name := strings.TrimSpace(group.Name)
		if name == "" {
			continue
		}
		for _, item := range group.Items {
			if _, ok := channelIDs[item.ChannelID]; !ok {
				continue
			}
			channel, ok := channelCache.Get(item.ChannelID)
			if !ok || !channel.Enabled {
				continue
			}
			// 渠道开启"仅暴露别名"后，原始模型对应的分组条目不再把
			// 该分组名暴露给客户端；仍允许别名条目对应的分组。
			if channel.ModelRedirectOnly && !channel.IsModelExposed(item.ModelName) {
				continue
			}
			modelSet[name] = struct{}{}
			break
		}
	}

	// 来源 2：渠道直接暴露的模型（含重定向别名，遵守仅别名开关）
	channelModels, err := ChannelListModelByChannelIDs(channelIDs, ctx)
	if err != nil {
		return nil, err
	}
	for _, name := range channelModels {
		modelSet[name] = struct{}{}
	}

	models := make([]string, 0, len(modelSet))
	for name := range modelSet {
		models = append(models, name)
	}
	sort.Strings(models)
	return models, nil
}

// ChannelListModelByChannelIDs 仅返回白名单内渠道自身暴露的模型
// （Model/CustomModel + 重定向别名，遵守 ModelRedirectOnly），
// 不包含分组名。标签受限的 API Key 使用此口径。
func ChannelListModelByChannelIDs(channelIDs map[int]struct{}, ctx context.Context) ([]string, error) {
	modelSet := make(map[string]struct{})
	for channelID := range channelIDs {
		channel, ok := channelCache.Get(channelID)
		if !ok || !channel.Enabled {
			continue
		}
		for _, name := range channel.ExposedModelNames() {
			modelSet[name] = struct{}{}
		}
	}
	models := make([]string, 0, len(modelSet))
	for name := range modelSet {
		models = append(models, name)
	}
	sort.Strings(models)
	return models, nil
}

// channelIDsForAPIKeyTags 将 API Key 的标签白名单解析为渠道 ID 集合。
// restricted 为 false 表示未配置标签白名单；配置后即使没有渠道命中也保持
// 白名单语义（与 supported_channels 一致，避免无效配置回退为不限渠道）。
func channelIDsForAPIKeyTags(key model.APIKey) (allowed map[int]struct{}, restricted bool) {
	tags := model.NormalizeAPIKeyTags(key.SupportedTags)
	if len(tags) == 0 {
		return nil, false
	}
	allowed = ChannelIDsForTags(tags)
	if channelIDs, channelRestricted := key.SupportedChannelIDSet(); channelRestricted {
		intersection := make(map[int]struct{}, len(allowed))
		for id := range allowed {
			if _, ok := channelIDs[id]; ok {
				intersection[id] = struct{}{}
			}
		}
		return intersection, true
	}
	return allowed, true
}

// GroupListModelForAPIKey 返回 API Key 实际可调用的模型列表：
// 标签白名单优先（只统计标签渠道自身暴露的模型，不纳入分组名），
// 否则按渠道白名单收窄，再按模型白名单过滤，限制之间取交集。
func GroupListModelForAPIKey(key model.APIKey, ctx context.Context) ([]string, error) {
	var models []string
	var err error
	if channelIDs, restricted := channelIDsForAPIKeyTags(key); restricted {
		// 标签受限时只返回渠道自身暴露的模型，不包含"分组"的同名模型。
		models, err = ChannelListModelByChannelIDs(channelIDs, ctx)
	} else if channelIDs, restricted := key.SupportedChannelIDSet(); restricted {
		models, err = GroupListModelByChannelIDs(channelIDs, ctx)
	} else {
		models, err = GroupListModel(ctx)
	}
	if err != nil {
		return nil, err
	}

	supportedModels := strings.TrimSpace(key.SupportedModels)
	if supportedModels == "" {
		return models, nil
	}
	allowed := make(map[string]struct{})
	for _, part := range strings.Split(supportedModels, ",") {
		if name := strings.TrimSpace(part); name != "" {
			allowed[name] = struct{}{}
		}
	}
	filtered := make([]string, 0, len(models))
	for _, name := range models {
		if _, ok := allowed[name]; ok {
			filtered = append(filtered, name)
		}
	}
	return filtered, nil
}

func GroupGet(id int, ctx context.Context) (*model.Group, error) {
	group, ok := groupCache.Get(id)
	if !ok {
		return nil, fmt.Errorf("group not found")
	}
	return &group, nil
}

func GroupGetEnabledMap(name string, ctx context.Context) (model.Group, error) {
	group, ok := groupMap.Get(name)
	if !ok {
		return model.Group{}, fmt.Errorf("group not found")
	}
	if len(group.Items) == 0 {
		group.Items = nil
		return group, nil
	}

	enabledItems := make([]model.GroupItem, 0, len(group.Items))
	for _, item := range group.Items {
		channel, ok := channelCache.Get(item.ChannelID)
		if !ok || !channel.Enabled {
			continue
		}
		enabledItems = append(enabledItems, item)
	}
	group.Items = enabledItems
	return group, nil
}

func GroupCreate(group *model.Group, ctx context.Context) error {
	if err := db.GetDB().WithContext(ctx).Create(group).Error; err != nil {
		return err
	}
	groupCache.Set(group.ID, *group)
	groupMap.Set(group.Name, *group)
	return nil
}

func GroupUpdate(req *model.GroupUpdateRequest, ctx context.Context) (*model.Group, error) {
	oldGroup, ok := groupCache.Get(req.ID)
	if !ok {
		return nil, fmt.Errorf("group not found")
	}
	oldName := oldGroup.Name
	affectedChannelIDs := groupUpdateAffectedChannelIDs(oldGroup, req)

	tx := db.GetDB().WithContext(ctx).Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	var selectFields []string
	updates := model.Group{ID: req.ID}

	if req.Name != nil {
		selectFields = append(selectFields, "name")
		updates.Name = *req.Name
	}
	if req.Mode != nil {
		selectFields = append(selectFields, "mode")
		updates.Mode = *req.Mode
	}
	if req.MatchRegex != nil {
		selectFields = append(selectFields, "match_regex")
		updates.MatchRegex = *req.MatchRegex
	}
	if req.FirstTokenTimeOut != nil {
		selectFields = append(selectFields, "first_token_time_out")
		updates.FirstTokenTimeOut = *req.FirstTokenTimeOut
	}
	if req.SessionKeepTime != nil {
		selectFields = append(selectFields, "session_keep_time")
		updates.SessionKeepTime = *req.SessionKeepTime
	}
	if req.RetryEnabled != nil {
		selectFields = append(selectFields, "retry_enabled")
		updates.RetryEnabled = *req.RetryEnabled
	}
	if req.MaxRetries != nil {
		v := *req.MaxRetries
		if v <= 0 {
			v = 3
		}
		selectFields = append(selectFields, "max_retries")
		updates.MaxRetries = v
	}

	if len(selectFields) > 0 {
		if err := tx.Model(&model.Group{}).Where("id = ?", req.ID).Select(selectFields).Updates(&updates).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to update group: %w", err)
		}
	}

	// 删除 items
	if len(req.ItemsToDelete) > 0 {
		if err := tx.Where("id IN ? AND group_id = ?", req.ItemsToDelete, req.ID).Delete(&model.GroupItem{}).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to delete items: %w", err)
		}
	}

	// 批量更新 items
	if len(req.ItemsToUpdate) > 0 {
		ids := make([]int, len(req.ItemsToUpdate))
		priorityCase := "CASE id"
		weightCase := "CASE id"
		for i, item := range req.ItemsToUpdate {
			ids[i] = item.ID
			priorityCase += fmt.Sprintf(" WHEN %d THEN %d", item.ID, item.Priority)
			weightCase += fmt.Sprintf(" WHEN %d THEN %d", item.ID, item.Weight)
		}
		priorityCase += " END"
		weightCase += " END"

		if err := tx.Model(&model.GroupItem{}).
			Where("id IN ? AND group_id = ?", ids, req.ID).
			Updates(map[string]interface{}{
				"priority": gorm.Expr(priorityCase),
				"weight":   gorm.Expr(weightCase),
			}).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to update items: %w", err)
		}
	}

	// 批量新增 items
	if len(req.ItemsToAdd) > 0 {
		newItems := make([]model.GroupItem, len(req.ItemsToAdd))
		for i, item := range req.ItemsToAdd {
			newItems[i] = model.GroupItem{
				GroupID:   req.ID,
				ChannelID: item.ChannelID,
				ModelName: item.ModelName,
				Priority:  item.Priority,
				Weight:    item.Weight,
			}
		}
		if err := tx.Create(&newItems).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to create items: %w", err)
		}
	}

	// 若该 Group 有 active preset，把当前实时状态回写到 preset（live binding）
	if err := syncActivePresetTx(tx, req.ID); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to sync active preset: %w", err)
	}

	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// 刷新缓存并返回最新数据
	if err := groupRefreshCacheByID(req.ID, ctx); err != nil {
		return nil, err
	}

	group, _ := groupCache.Get(req.ID)
	if oldName != "" && oldName != group.Name {
		groupMap.Del(oldName)
	}
	resetBalancerStateForChannels(affectedChannelIDs...)
	return &group, nil
}

func groupUpdateAffectedChannelIDs(oldGroup model.Group, req *model.GroupUpdateRequest) []int {
	itemChannels := make(map[int]int, len(oldGroup.Items))
	for _, item := range oldGroup.Items {
		itemChannels[item.ID] = item.ChannelID
	}

	ids := make([]int, 0, len(oldGroup.Items)+len(req.ItemsToAdd))
	if req.Mode != nil || req.SessionKeepTime != nil {
		for _, item := range oldGroup.Items {
			ids = append(ids, item.ChannelID)
		}
	}
	if req.RetryEnabled != nil || req.MaxRetries != nil {
		for _, item := range oldGroup.Items {
			ids = append(ids, item.ChannelID)
		}
	}
	for _, itemID := range req.ItemsToDelete {
		ids = append(ids, itemChannels[itemID])
	}
	for _, item := range req.ItemsToUpdate {
		ids = append(ids, itemChannels[item.ID])
	}
	for _, item := range req.ItemsToAdd {
		ids = append(ids, item.ChannelID)
	}
	return ids
}

func GroupDel(id int, ctx context.Context) error {
	group, ok := groupCache.Get(id)
	if !ok {
		return fmt.Errorf("group not found")
	}

	tx := db.GetDB().WithContext(ctx).Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	if err := tx.Where("group_id = ?", id).Delete(&model.GroupItem{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete group items: %w", err)
	}

	if err := tx.Where("group_id = ?", id).Delete(&model.GroupPreset{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete group presets: %w", err)
	}

	if err := tx.Delete(&model.Group{}, id).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete group: %w", err)
	}

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	groupCache.Del(id)
	groupMap.Del(group.Name)
	for _, item := range group.Items {
		resetBalancerStateForChannel(item.ChannelID)
	}
	return nil
}

func GroupItemAdd(item *model.GroupItem, ctx context.Context) error {
	if _, ok := groupCache.Get(item.GroupID); !ok {
		return fmt.Errorf("group not found")
	}

	if err := db.GetDB().WithContext(ctx).Create(item).Error; err != nil {
		return err
	}

	if err := groupRefreshCacheByID(item.GroupID, ctx); err != nil {
		return err
	}
	resetBalancerStateForChannel(item.ChannelID)
	return nil
}

func GroupItemBatchAdd(groupID int, items []model.GroupIDAndLLMName, ctx context.Context) error {
	if len(items) == 0 {
		return nil
	}

	group, ok := groupCache.Get(groupID)
	if !ok {
		return fmt.Errorf("group not found")
	}

	seen := make(map[string]struct{}, len(items))
	uniq := make([]model.GroupIDAndLLMName, 0, len(items))
	for _, it := range items {
		if it.ChannelID == 0 || it.ModelName == "" {
			continue
		}
		k := fmt.Sprintf("%d|%s", it.ChannelID, it.ModelName)
		if _, exists := seen[k]; exists {
			continue
		}
		seen[k] = struct{}{}
		uniq = append(uniq, it)
	}
	if len(uniq) == 0 {
		return nil
	}

	nextPriority := 1
	for _, gi := range group.Items {
		if gi.Priority >= nextPriority {
			nextPriority = gi.Priority + 1
		}
	}

	newItems := make([]model.GroupItem, 0, len(uniq))
	for _, it := range uniq {
		newItems = append(newItems, model.GroupItem{
			GroupID:   groupID,
			ChannelID: it.ChannelID,
			ModelName: it.ModelName,
			Priority:  nextPriority,
			Weight:    1,
		})
		nextPriority++
	}

	if err := db.GetDB().WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "group_id"}, {Name: "channel_id"}, {Name: "model_name"}},
			DoNothing: true,
		}).
		Create(&newItems).Error; err != nil {
		return fmt.Errorf("failed to create group items: %w", err)
	}

	if err := groupRefreshCacheByID(groupID, ctx); err != nil {
		return err
	}
	channelIDs := make([]int, 0, len(uniq))
	for _, item := range uniq {
		channelIDs = append(channelIDs, item.ChannelID)
	}
	resetBalancerStateForChannels(channelIDs...)
	return nil
}

// GroupItemBatchDelByChannelAndModels 根据渠道ID和模型名称批量删除分组项
func GroupItemBatchDelByChannelAndModels(keys []model.GroupIDAndLLMName, ctx context.Context) error {
	if len(keys) == 0 {
		return nil
	}

	conditions := make([][]interface{}, len(keys))
	for i, key := range keys {
		conditions[i] = []interface{}{key.ChannelID, key.ModelName}
	}

	var groupIDs []int
	if err := db.GetDB().WithContext(ctx).
		Model(&model.GroupItem{}).
		Distinct("group_id").
		Where("(channel_id, model_name) IN ?", conditions).
		Pluck("group_id", &groupIDs).Error; err != nil {
		return fmt.Errorf("failed to find group ids: %w", err)
	}

	if len(groupIDs) == 0 {
		return nil
	}

	if err := db.GetDB().WithContext(ctx).
		Where("(channel_id, model_name) IN ?", conditions).
		Delete(&model.GroupItem{}).Error; err != nil {
		return fmt.Errorf("failed to delete group items: %w", err)
	}

	if err := groupRefreshCacheByIDs(groupIDs, ctx); err != nil {
		return fmt.Errorf("failed to refresh group cache: %w", err)
	}

	channelIDs := make([]int, 0, len(keys))
	for _, key := range keys {
		channelIDs = append(channelIDs, key.ChannelID)
	}
	resetBalancerStateForChannels(channelIDs...)
	return nil
}

func GroupItemList(groupID int, ctx context.Context) ([]model.GroupItem, error) {
	var items []model.GroupItem
	if err := db.GetDB().WithContext(ctx).
		Where("group_id = ?", groupID).
		Order("priority ASC").
		Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func groupRefreshCache(ctx context.Context) error {
	groups := []model.Group{}
	if err := db.GetDB().WithContext(ctx).
		Preload("Items").
		Find(&groups).Error; err != nil {
		return err
	}
	for _, group := range groups {
		groupCache.Set(group.ID, group)
		groupMap.Set(group.Name, group)
	}
	return nil
}

func groupRefreshCacheByID(id int, ctx context.Context) error {
	var group model.Group
	if err := db.GetDB().WithContext(ctx).
		Preload("Items").
		First(&group, id).Error; err != nil {
		return err
	}
	groupCache.Set(group.ID, group)
	groupMap.Set(group.Name, group)
	return nil
}

func groupRefreshCacheByIDs(ids []int, ctx context.Context) error {
	if len(ids) == 0 {
		return nil
	}
	var groups []model.Group
	if err := db.GetDB().WithContext(ctx).
		Preload("Items").
		Where("id IN ?", ids).
		Find(&groups).Error; err != nil {
		return err
	}
	for _, group := range groups {
		groupCache.Set(group.ID, group)
		groupMap.Set(group.Name, group)
	}
	return nil
}

func GroupRefreshCacheByIDs(ids []int, ctx context.Context) error {
	return groupRefreshCacheByIDs(ids, ctx)
}
