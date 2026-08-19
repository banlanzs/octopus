package op

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/bestruirui/octopus/internal/apperror"
	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
)

const (
	codeChannelKeysImportInvalidRequest = "channel.keys_import.invalid_request"
	codeChannelKeysImportNotFound       = "channel.keys_import.not_found"
	codeChannelKeysImportManaged        = "channel.keys_import.managed"
	codeChannelKeysImportFailed         = "channel.keys_import.failed"
)

func newChannelKeysImportInvalidRequestError(message string) *apperror.Error {
	return apperror.New(codeChannelKeysImportInvalidRequest, message).WithStatus(http.StatusBadRequest)
}

func newChannelKeysImportNotFoundError() *apperror.Error {
	return apperror.New(codeChannelKeysImportNotFound, "channel not found").WithStatus(http.StatusNotFound)
}

func newChannelKeysImportManagedError() *apperror.Error {
	return apperror.New(codeChannelKeysImportManaged, "managed site channel is read-only; please edit keys from the site account").WithStatus(http.StatusBadRequest)
}

func wrapChannelKeysImportFailedError(err error) *apperror.Error {
	return apperror.Wrap(codeChannelKeysImportFailed, "channel keys import failed", err).WithStatus(http.StatusInternalServerError)
}

// ChannelImportKeys 向指定渠道批量导入 API Key。
//
// 解析后的 key 会先按原文去重，再与渠道已有 key 去重；仅新增未重复的 key。
// 请求中的 Content（批量粘贴文本）和 Keys（结构化数组）可以同时提供。
func ChannelImportKeys(req *model.ChannelKeyImportRequest, ctx context.Context) (*model.ChannelKeyImportResult, error) {
	if req == nil || req.ID <= 0 {
		return nil, newChannelKeysImportInvalidRequestError("channel id is required")
	}

	existingChannel, ok := channelCache.Get(req.ID)
	if !ok {
		return nil, newChannelKeysImportNotFoundError()
	}

	if _, managed, err := ChannelManagedBinding(req.ID, ctx); err != nil {
		return nil, wrapChannelKeysImportFailedError(err)
	} else if managed {
		return nil, newChannelKeysImportManagedError()
	}

	// 先解析并合并两类输入，得到按原文去重后的候选 key 列表。
	candidates := make([]string, 0, len(req.Keys)+16)
	seen := make(map[string]struct{}, len(req.Keys)+16)
	duplicates := 0

	addCandidate := func(raw string) {
		key := model.NormalizeChannelKeyImportItem(raw)
		if key == "" {
			return
		}
		if _, exists := seen[key]; exists {
			duplicates++
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, key)
	}

	if strings.TrimSpace(req.Content) != "" {
		parsedKeys, parsedDuplicates := model.ParseChannelKeyImportContent(req.Content)
		duplicates += parsedDuplicates
		for _, key := range parsedKeys {
			addCandidate(key)
		}
	}
	for _, raw := range req.Keys {
		addCandidate(raw)
	}

	if len(candidates) == 0 {
		return nil, newChannelKeysImportInvalidRequestError("no valid api keys found in import request")
	}
	if len(candidates) > model.MaxChannelKeyImportBatchSize {
		return nil, newChannelKeysImportInvalidRequestError("too many api keys in one import request")
	}

	existingKeys := make(map[string]struct{}, len(existingChannel.Keys))
	for _, key := range existingChannel.Keys {
		channelKey := strings.TrimSpace(key.ChannelKey)
		if channelKey != "" {
			existingKeys[channelKey] = struct{}{}
		}
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	remark := strings.TrimSpace(req.Remark)

	newKeys := make([]model.ChannelKey, 0, len(candidates))
	for _, key := range candidates {
		if _, exists := existingKeys[key]; exists {
			duplicates++
			continue
		}
		newKeys = append(newKeys, model.ChannelKey{
			ChannelID:  req.ID,
			Enabled:    enabled,
			ChannelKey: key,
			Remark:     remark,
		})
	}

	if len(newKeys) > 0 {
		// 使用 map 批量插入而不是直接 Create 结构体：ChannelKey.Enabled 带
		// gorm default tag，结构体零值 false 会被 GORM 跳过并落入 DB 默认值 true。
		rows := make([]map[string]interface{}, 0, len(newKeys))
		for _, key := range newKeys {
			rows = append(rows, map[string]interface{}{
				"channel_id":          key.ChannelID,
				"enabled":             key.Enabled,
				"channel_key":         key.ChannelKey,
				"status_code":         0,
				"last_use_time_stamp": 0,
				"total_cost":          0,
				"remark":              key.Remark,
			})
		}

		tx := db.GetDB().WithContext(ctx).Begin()
		if tx.Error != nil {
			return nil, wrapChannelKeysImportFailedError(tx.Error)
		}
		for start := 0; start < len(rows); start += 100 {
			end := start + 100
			if end > len(rows) {
				end = len(rows)
			}
			batch := rows[start:end]
			if err := tx.Model(&model.ChannelKey{}).Create(&batch).Error; err != nil {
				tx.Rollback()
				return nil, wrapChannelKeysImportFailedError(err)
			}
		}
		if err := tx.Commit().Error; err != nil {
			return nil, wrapChannelKeysImportFailedError(err)
		}
	}

	// 刷新缓存，保证渠道列表/后续调度立即可见新 key。
	if err := channelRefreshCacheByID(req.ID, ctx); err != nil {
		return nil, wrapChannelKeysImportFailedError(err)
	}

	channel, ok := channelCache.Get(req.ID)
	if !ok {
		return nil, wrapChannelKeysImportFailedError(fmt.Errorf("channel disappeared from cache after import"))
	}
	normalizeChannelProxyFields(&channel)

	return &model.ChannelKeyImportResult{
		Imported:   len(newKeys),
		Duplicated: duplicates,
		Channel:    &channel,
	}, nil
}
