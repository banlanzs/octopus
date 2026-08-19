package handlers

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/channeltest"
	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/bestruirui/octopus/internal/task"
	"github.com/bestruirui/octopus/internal/utils/safe"
	"github.com/gin-gonic/gin"
)

func init() {
	router.NewGroupRouter("/api/v1/channel").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(listChannel),
		).
		AddRoute(
			router.NewRoute("/create", http.MethodPost).
				Handle(createChannel),
		).
		AddRoute(
			router.NewRoute("/update", http.MethodPost).
				Handle(updateChannel),
		).
		AddRoute(
			router.NewRoute("/enable", http.MethodPost).
				Handle(enableChannel),
		).
		AddRoute(
			router.NewRoute("/delete/:id", http.MethodDelete).
				Handle(deleteChannel),
		).
		AddRoute(
			router.NewRoute("/fetch-model", http.MethodPost).
				Handle(fetchModel),
		).
		AddRoute(
			router.NewRoute("/test", http.MethodPost).
				Handle(testChannel),
		).
		AddRoute(
			router.NewRoute("/import-keys", http.MethodPost).
				Handle(importChannelKeys),
		)
	router.NewGroupRouter("/api/v1/channel").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/sync", http.MethodPost).
				Handle(syncChannel),
		).
		AddRoute(
			router.NewRoute("/last-sync-time", http.MethodGet).
				Handle(getLastSyncTime),
		)
}

func listChannel(c *gin.Context) {
	channels, err := op.ChannelList(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	channelIDs := make([]int, 0, len(channels))
	for _, channel := range channels {
		channelIDs = append(channelIDs, channel.ID)
	}
	bindingMap, err := op.SiteChannelBindingMapByChannelIDs(channelIDs, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	for i, channel := range channels {
		stats := op.StatsChannelGet(channel.ID)
		channels[i].Stats = &stats
		if binding, ok := bindingMap[channel.ID]; ok {
			channels[i].Managed = true
			channels[i].ManagedSource = &model.ManagedChannelSource{
				SiteID:          binding.SiteID,
				SiteAccountID:   binding.SiteAccountID,
				SiteUserGroupID: binding.SiteUserGroupID,
				GroupKey:        binding.GroupKey,
			}
		}
	}
	resp.Success(c, channels)
}

func createChannel(c *gin.Context) {
	var channel model.Channel
	if err := c.ShouldBindJSON(&channel); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if channel.ProxyMode == "" {
		channel.ProxyMode = model.ProxyUsageModeDirect
	}
	if err := channel.ProxyMode.Validate(false); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if channel.ProxyMode == model.ProxyUsageModePool && (channel.ProxyConfigID == nil || *channel.ProxyConfigID <= 0) {
		resp.Error(c, http.StatusBadRequest, "proxy config id is required when proxy mode is pool")
		return
	}
	if channel.ProxyMode == model.ProxyUsageModePool {
		if _, err := op.ProxyURLForConfig(*channel.ProxyConfigID, c.Request.Context()); err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
	}
	if channel.ProxyMode != model.ProxyUsageModePool {
		channel.ProxyConfigID = nil
	}
	if err := op.ChannelCreate(&channel, c.Request.Context()); err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, channelError(codeChannelCreateFailed, "channel create failed", err))
		return
	}
	stats := op.StatsChannelGet(channel.ID)
	channel.Stats = &stats
	createdChannel := channel
	safe.Go("channel-create-postprocess", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		modelStr := createdChannel.Model + "," + createdChannel.CustomModel
		modelArray := strings.Split(modelStr, ",")
		helper.LLMPriceAddToDB(modelArray, ctx)
		helper.EnsureChannelModelPrices(createdChannel.ID, modelArray, ctx)
		helper.ChannelBaseUrlDelayUpdate(&createdChannel, ctx)
		helper.ChannelAutoGroup(&createdChannel, ctx)
	})
	resp.Success(c, channel)
}

func updateChannel(c *gin.Context) {
	var req model.ChannelUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	channel, err := op.ChannelUpdate(&req, c.Request.Context())
	if err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, channelError(codeChannelUpdateFailed, "channel update failed", err))
		return
	}
	stats := op.StatsChannelGet(channel.ID)
	channel.Stats = &stats
	updatedChannel := *channel
	safe.Go("channel-update-postprocess", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		modelStr := updatedChannel.Model + "," + updatedChannel.CustomModel
		modelArray := strings.Split(modelStr, ",")
		helper.LLMPriceAddToDB(modelArray, ctx)
		helper.EnsureChannelModelPrices(updatedChannel.ID, modelArray, ctx)
		helper.ChannelBaseUrlDelayUpdate(&updatedChannel, ctx)
		helper.ChannelAutoGroup(&updatedChannel, ctx)
	})
	resp.Success(c, channel)
}

func enableChannel(c *gin.Context) {
	var request struct {
		ID      int  `json:"id"`
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if err := op.ChannelEnabled(request.ID, request.Enabled, c.Request.Context()); err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, channelError(codeChannelUpdateFailed, "channel update failed", err))
		return
	}
	resp.Success(c, nil)
}

func deleteChannel(c *gin.Context) {
	id := c.Param("id")
	idNum, err := strconv.Atoi(id)
	if err != nil {
		resp.InvalidParam(c)
		return
	}
	if err := op.ChannelDel(idNum, c.Request.Context()); err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, channelError(codeChannelDeleteFailed, "channel delete failed", err))
		return
	}
	resp.Success(c, nil)
}
func fetchModel(c *gin.Context) {
	var request model.Channel
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.InvalidJSON(c)
		return
	}
	models, err := helper.FetchModels(c.Request.Context(), request)
	if err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, channelError(codeChannelFetchModelsFailed, "channel fetch models failed", err))
		return
	}
	resp.Success(c, models)
}

// testChannel 使用渠道当前（或表单草稿）配置向上游发送一条极小非流式请求。
// 测试失败仍返回 HTTP 200，由 data.success/data.error 表达结果，
// 前端据此展示详细状态而非抛出统一接口错误。
func testChannel(c *gin.Context) {
	var channel model.Channel
	if err := c.ShouldBindJSON(&channel); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if channel.ProxyMode == "" {
		channel.ProxyMode = model.ProxyUsageModeDirect
	}
	if err := channel.ProxyMode.Validate(false); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if channel.ProxyMode == model.ProxyUsageModePool {
		if channel.ProxyConfigID == nil || *channel.ProxyConfigID <= 0 {
			resp.Error(c, http.StatusBadRequest, "proxy config id is required when proxy mode is pool")
			return
		}
		if _, err := op.ProxyURLForConfig(*channel.ProxyConfigID, c.Request.Context()); err != nil {
			resp.Error(c, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		channel.ProxyConfigID = nil
	}
	result := channeltest.Run(c.Request.Context(), &channel)
	resp.Success(c, result)
}

// importChannelKeys 向已存在的渠道批量导入 API Key。
// 支持粘贴文本（换行/逗号/分号/Tab 分隔或 JSON 数组）和结构化 keys 数组。
func importChannelKeys(c *gin.Context) {
	var req model.ChannelKeyImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if req.ID <= 0 {
		resp.Error(c, http.StatusBadRequest, "channel id is required")
		return
	}
	if strings.TrimSpace(req.Content) == "" && len(req.Keys) == 0 {
		resp.Error(c, http.StatusBadRequest, "content or keys is required")
		return
	}
	result, err := op.ChannelImportKeys(&req, c.Request.Context())
	if err != nil {
		resp.ErrorWithAppError(c, http.StatusBadRequest, err)
		return
	}
	if result.Channel != nil {
		stats := op.StatsChannelGet(result.Channel.ID)
		result.Channel.Stats = &stats
	}
	resp.Success(c, result)
}

func syncChannel(c *gin.Context) {
	task.SyncModelsTask()
	resp.Success(c, nil)
}

func getLastSyncTime(c *gin.Context) {
	time := task.GetLastSyncModelsTime()
	resp.Success(c, time)
}
