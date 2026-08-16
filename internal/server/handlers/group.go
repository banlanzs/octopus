package handlers

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/bestruirui/octopus/internal/apperror"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
	"github.com/dlclark/regexp2"
	"github.com/gin-gonic/gin"
)

func init() {
	router.NewGroupRouter("/api/v1/group").
		Use(middleware.Auth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(getGroupList),
		).
		AddRoute(
			router.NewRoute("/create", http.MethodPost).
				Handle(createGroup),
		).
		AddRoute(
			router.NewRoute("/update", http.MethodPost).
				Handle(updateGroup),
		).
		AddRoute(
			router.NewRoute("/delete/:id", http.MethodDelete).
				Handle(deleteGroup),
		).
		AddRoute(
			router.NewRoute("/export/:id", http.MethodGet).
				Handle(exportGroup),
		)
}

func getGroupList(c *gin.Context) {
	groups, err := op.GroupList(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	for groupIndex := range groups {
		if groups[groupIndex].Mode != model.GroupModeAuto {
			continue
		}
		groups[groupIndex].Items = append([]model.GroupItem(nil), groups[groupIndex].Items...)
		for itemIndex := range groups[groupIndex].Items {
			item := &groups[groupIndex].Items[itemIndex]
			health := balancer.AutoRankHealthForGroupItems(groups[groupIndex].ID, item.ChannelID, item.ModelName, groups[groupIndex].Items)
			item.AutoRank = &health
		}
	}
	resp.Success(c, groups)
}

func createGroup(c *gin.Context) {
	var group model.Group
	if err := c.ShouldBindJSON(&group); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if group.MatchRegex != "" {
		_, err := regexp2.Compile(group.MatchRegex, regexp2.ECMAScript)
		if err != nil {
			resp.ErrorWithAppError(c, http.StatusBadRequest, apperror.New(apperror.CodeCommonValidationFailed, err.Error()).WithStatus(http.StatusBadRequest))
			return
		}
	}
	if err := op.GroupCreate(&group, c.Request.Context()); err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, groupError(codeGroupCreateFailed, "group create failed", err))
		return
	}
	resp.Success(c, group)
}

func updateGroup(c *gin.Context) {
	var req model.GroupUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.InvalidJSON(c)
		return
	}
	if req.MatchRegex != nil {
		_, err := regexp2.Compile(*req.MatchRegex, regexp2.ECMAScript)
		if err != nil {
			resp.ErrorWithAppError(c, http.StatusBadRequest, apperror.New(apperror.CodeCommonValidationFailed, err.Error()).WithStatus(http.StatusBadRequest))
			return
		}
	}
	group, err := op.GroupUpdate(&req, c.Request.Context())
	if err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, groupError(codeGroupUpdateFailed, "group update failed", err))
		return
	}
	resp.Success(c, group)
}

func deleteGroup(c *gin.Context) {
	id := c.Param("id")
	idNum, err := strconv.Atoi(id)
	if err != nil {
		resp.InvalidParam(c)
		return
	}
	if err := op.GroupDel(idNum, c.Request.Context()); err != nil {
		resp.ErrorWithAppError(c, http.StatusInternalServerError, groupError(codeGroupDeleteFailed, "group delete failed", err))
		return
	}
	resp.Success(c, "group deleted successfully")
}

// ===== 分组导出（调试用） =====

type groupExportResponse struct {
	ExportedAt string            `json:"exported_at"`
	Group      groupExportInfo   `json:"group"`
	Items      []groupExportItem `json:"items"`
}

type groupExportInfo struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	Mode              int    `json:"mode"`
	ModeLabel         string `json:"mode_label"`
	MatchRegex        string `json:"match_regex"`
	FirstTokenTimeOut int    `json:"first_token_time_out"`
	SessionKeepTime   int    `json:"session_keep_time"`
	RetryEnabled      bool   `json:"retry_enabled"`
	MaxRetries        int    `json:"max_retries"`
	Pinned            bool   `json:"pinned"`
	ActivePresetID    *int   `json:"active_preset_id,omitempty"`
}

type groupExportItem struct {
	ItemID    int                 `json:"item_id"`
	ChannelID int                 `json:"channel_id"`
	ModelName string              `json:"model_name"`
	Priority  int                 `json:"priority"`
	Weight    int                 `json:"weight"`
	Channel   *groupExportChannel `json:"channel,omitempty"`
	// ChannelError 渠道加载失败时的原因（渠道可能已被删除）。
	ChannelError string                  `json:"channel_error,omitempty"`
	Health       model.LLMAutoRankHealth `json:"health"`
}

type groupExportChannel struct {
	ID                    int                     `json:"id"`
	Name                  string                  `json:"name"`
	Type                  int                     `json:"type"`
	TypeLabel             string                  `json:"type_label"`
	Enabled               bool                    `json:"enabled"`
	BaseUrls              []string                `json:"base_urls"`
	Models                string                  `json:"models"`
	ForceDeepSeekThinking bool                    `json:"force_deep_seek_thinking"`
	SchedulingExempt      bool                    `json:"scheduling_exempt"`
	Managed               bool                    `json:"managed"`
	ParamOverride         *string                 `json:"param_override,omitempty"`
	Keys                  []groupExportChannelKey `json:"keys"`
	// PORRetired 被动离群退役状态；未退役/无记录时省略。
	PORRetired *model.SiteChannelOutlierState `json:"por_retired,omitempty"`
}

type groupExportChannelKey struct {
	ID                   int    `json:"id"`
	Enabled              bool   `json:"enabled"`
	Remark               string `json:"remark"`
	KeyMasked            string `json:"key_masked"` // 脱敏：sk-****abcd
	Tripped              bool   `json:"tripped"`
	RemainingCooldownSec int64  `json:"remaining_cooldown_sec"`
}

// exportGroup 导出分组的调度策略 + 每个条目的模型/渠道/健康度摘要，
// 供调试路由问题时离线分析。
func exportGroup(c *gin.Context) {
	idNum, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resp.InvalidParam(c)
		return
	}
	group, err := op.GroupGet(idNum, c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusNotFound, "group not found")
		return
	}

	exported := &groupExportResponse{
		ExportedAt: time.Now().Format(time.RFC3339),
		Group: groupExportInfo{
			ID:                group.ID,
			Name:              group.Name,
			Mode:              int(group.Mode),
			ModeLabel:         groupModeLabel(group.Mode),
			MatchRegex:        group.MatchRegex,
			FirstTokenTimeOut: group.FirstTokenTimeOut,
			SessionKeepTime:   group.SessionKeepTime,
			RetryEnabled:      group.RetryEnabled,
			MaxRetries:        group.MaxRetries,
			Pinned:            group.Pinned,
			ActivePresetID:    group.ActivePresetID,
		},
	}

	items := append([]model.GroupItem(nil), group.Items...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		return items[i].ID < items[j].ID
	})

	for _, item := range items {
		exportItem := groupExportItem{
			ItemID:    item.ID,
			ChannelID: item.ChannelID,
			ModelName: item.ModelName,
			Priority:  item.Priority,
			Weight:    item.Weight,
			Health:    balancer.AutoRankHealthForGroupItems(group.ID, item.ChannelID, item.ModelName, group.Items),
		}
		if ch, getErr := op.ChannelGet(item.ChannelID, c.Request.Context()); getErr == nil {
			exportItem.Channel = buildGroupExportChannel(ch, item, c.Request.Context())
		} else {
			exportItem.ChannelError = getErr.Error()
		}
		exported.Items = append(exported.Items, exportItem)
	}

	resp.Success(c, exported)
}

func buildGroupExportChannel(ch *model.Channel, item model.GroupItem, ctx context.Context) *groupExportChannel {
	out := &groupExportChannel{
		ID:                    ch.ID,
		Name:                  ch.Name,
		Type:                  int(ch.Type),
		TypeLabel:             outboundTypeLabel(ch.Type),
		Enabled:               ch.Enabled,
		BaseUrls:              baseURLList(ch.BaseUrls),
		Models:                ch.Model,
		ForceDeepSeekThinking: ch.ForceDeepSeekThinking,
		SchedulingExempt:      ch.SchedulingExempt,
		Managed:               ch.Managed,
		ParamOverride:         ch.ParamOverride,
	}
	for _, key := range ch.Keys {
		tripped, remaining := balancer.IsTripped(item.ChannelID, key.ID, item.ModelName)
		out.Keys = append(out.Keys, groupExportChannelKey{
			ID:                   key.ID,
			Enabled:              key.Enabled,
			Remark:               key.Remark,
			KeyMasked:            maskChannelKey(key.ChannelKey),
			Tripped:              tripped,
			RemainingCooldownSec: int64(remaining.Seconds()),
		})
	}
	if por, err := op.SiteChannelOutlierGet(ch.ID, ctx); err == nil {
		out.PORRetired = por
	}
	return out
}

func baseURLList(urls []model.BaseUrl) []string {
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		out = append(out, u.URL)
	}
	return out
}

// maskChannelKey 脱敏渠道 key：sk-****abcd（保留前 3 + 后 4）。
func maskChannelKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:3] + "****" + key[len(key)-4:]
}

func groupModeLabel(m model.GroupMode) string {
	switch m {
	case model.GroupModeRoundRobin:
		return "round_robin"
	case model.GroupModeRandom:
		return "random"
	case model.GroupModeFailover:
		return "failover"
	case model.GroupModeWeighted:
		return "weighted"
	case model.GroupModeAuto:
		return "auto"
	default:
		return "unknown"
	}
}

func outboundTypeLabel(t outbound.OutboundType) string {
	switch t {
	case outbound.OutboundTypeOpenAIChat:
		return "openai_chat"
	case outbound.OutboundTypeOpenAIResponse:
		return "openai_response"
	case outbound.OutboundTypeAnthropic:
		return "anthropic"
	case outbound.OutboundTypeGemini:
		return "gemini"
	case outbound.OutboundTypeVolcengine:
		return "volcengine"
	case outbound.OutboundTypeOpenAIEmbedding:
		return "embedding"
	case outbound.OutboundTypeAuto:
		return "auto"
	default:
		return "unknown"
	}
}
