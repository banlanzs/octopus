package handlers

import (
	"net/http"
	"strings"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/price"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

func init() {
	router.NewGroupRouter("/api/v1/model").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(listLLM),
		).
		AddRoute(
			router.NewRoute("/create", http.MethodPost).
				Handle(createLLM),
		).
		AddRoute(
			router.NewRoute("/channel", http.MethodGet).
				Handle(listLLMByChannel),
		).
		AddRoute(
			router.NewRoute("/update", http.MethodPost).
				Handle(updateLLM),
		).
		AddRoute(
			router.NewRoute("/delete", http.MethodPost).
				Handle(deleteLLM),
		).
		AddRoute(
			router.NewRoute("/update-price", http.MethodPost).
				Handle(updateLLMPrice),
		).
		AddRoute(
			router.NewRoute("/channel-price/update", http.MethodPost).
				Handle(updateChannelModelPrice),
		).
		AddRoute(
			router.NewRoute("/channel-price/delete", http.MethodPost).
				Handle(deleteChannelModelPrice),
		).
		AddRoute(
			router.NewRoute("/last-update-time", http.MethodGet).
				Handle(getLastUpdateTime),
		)
	router.NewGroupRouter("/v1").
		Use(middleware.APIKeyAuth()).
		AddRoute(
			router.NewRoute("/models", http.MethodGet).
				Handle(getModelList),
		)
}

func getModelList(c *gin.Context) {
	models, err := op.GroupListModel(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	apiKeyId := c.GetInt("api_key_id")
	apiKey, err := op.APIKeyGet(apiKeyId, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	if apiKey.SupportedModels != "" {
		supportedModels := lo.Map(strings.Split(apiKey.SupportedModels, ","), func(s string, _ int) string {
			return strings.TrimSpace(s)
		})
		models = lo.Filter(models, func(m string, _ int) bool {
			return lo.Contains(supportedModels, m)
		})
	}

	if c.GetString("request_type") == "anthropic" {
		var anthropicModels []model.AnthropicModel
		for _, m := range models {
			anthropicModels = append(anthropicModels, model.AnthropicModel{
				ID:          m,
				CreatedAt:   "2024-01-01T00:00:00Z",
				DisplayName: m,
				Type:        "model",
			})
		}
		response := gin.H{
			"data":     anthropicModels,
			"has_more": false,
		}
		if len(anthropicModels) > 0 {
			response["first_id"] = anthropicModels[0].ID
			response["last_id"] = anthropicModels[len(anthropicModels)-1].ID
		}
		c.JSON(200, response)
	} else {
		var openAIModels []model.OpenAIModel
		for _, m := range models {
			openAIModels = append(openAIModels, model.OpenAIModel{
				ID:      m,
				Object:  "model",
				Created: 1763395200,
				OwnedBy: "octopus",
			})
		}
		c.JSON(200, gin.H{
			"success": true,
			"data":    openAIModels,
			"object":  "list",
		})
	}
}

func listLLM(c *gin.Context) {
	models, err := op.LLMList(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp.Success(c, models)
}

func listLLMByChannel(c *gin.Context) {
	channels, err := op.ChannelLLMList(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	// 附加自动排序健康度与熔断冷却摘要（只读）。所有模型都携带：
	// 无采样时 samples=0，前端展示"暂无采样（冷启动）"占位，方便观察
	// 哪些模型从未被调用。
	// 同时附加计费价格（渠道价优先，未配置时为全局兜底价）与渠道倍率，供价格页渲染。
	for i := range channels {
		p := price.GetChannelModelPrice(channels[i].ChannelID, channels[i].Name)
		h := balancer.AutoRankHealthFor(channels[i].ChannelID, channels[i].Name)
		channels[i].AutoRank = &h
		if p != nil {
			channels[i].Price = p
		}
		// 区分是否已单独配置渠道价（GetChannelModelPrice 在无配置时回退全局价，
		// 无法据此判断来源，需显式查询）。
		if _, err := op.ChannelModelPriceGet(channels[i].ChannelID, channels[i].Name); err == nil {
			channels[i].HasChannelPrice = true
		}
		// 附加渠道倍率
		if ch, err := op.ChannelGet(channels[i].ChannelID, c.Request.Context()); err == nil && ch.PriceMultiplier != 0 {
			channels[i].PriceMultiplier = ch.PriceMultiplier
		}
	}
	resp.Success(c, channels)
}

func createLLM(c *gin.Context) {
	var model model.LLMInfo
	if err := c.ShouldBindJSON(&model); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if err := op.LLMCreate(model, c.Request.Context()); err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, modelError(codeModelCreateFailed, "model create failed", err))
		return
	}
	resp.Success(c, model)
}

func updateLLM(c *gin.Context) {
	var model model.LLMInfo
	if err := c.ShouldBindJSON(&model); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if err := op.LLMUpdate(model, c.Request.Context()); err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, modelError(codeModelUpdateFailed, "model update failed", err))
		return
	}
	resp.Success(c, model)
}

func deleteLLM(c *gin.Context) {
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if err := op.LLMDelete(req.Name, c.Request.Context()); err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, modelError(codeModelPriceDeleteFailed, "model price delete failed", err))
		return
	}
	resp.Success(c, nil)
}

func updateLLMPrice(c *gin.Context) {
	err := price.UpdateLLMPrice(c.Request.Context())
	if err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, modelError(codeModelPriceUpdateFailed, "model price update failed", err))
		return
	}
	resp.Success(c, nil)
}

func getLastUpdateTime(c *gin.Context) {
	time := price.GetLastUpdateTime()
	resp.Success(c, time)
}

func updateChannelModelPrice(c *gin.Context) {
	var req struct {
		ChannelID  int     `json:"channel_id" binding:"required"`
		ModelName  string  `json:"model_name" binding:"required"`
		Input       float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if err := op.ChannelModelPriceUpsert(req.ChannelID, req.ModelName, model.LLMPrice{
		Input:      req.Input,
		Output:     req.Output,
		CacheRead:  req.CacheRead,
		CacheWrite: req.CacheWrite,
	}, c.Request.Context()); err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, modelError(codeChannelModelPriceUpdateFailed, "channel model price update failed", err))
		return
	}
	resp.Success(c, nil)
}

func deleteChannelModelPrice(c *gin.Context) {
	var req struct {
		ChannelID int    `json:"channel_id" binding:"required"`
		ModelName string `json:"model_name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if err := op.ChannelModelPriceDelete(req.ChannelID, req.ModelName, c.Request.Context()); err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, modelError(codeChannelModelPriceDeleteFailed, "channel model price delete failed", err))
		return
	}
	resp.Success(c, nil)
}
