package handlers

import (
	"net/http"

	"github.com/bestruirui/octopus/internal/relay"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
)

func init() {
	router.NewGroupRouter("/v1").
		Use(middleware.APIKeyAuth()).
		Use(middleware.RequireJSON()).
		AddRoute(
			router.NewRoute("/chat/completions", http.MethodPost).
				Handle(chat),
		).
		AddRoute(
			router.NewRoute("/responses", http.MethodPost).
				Handle(response),
		).
		AddRoute(
			router.NewRoute("/responses/compact", http.MethodPost).
				Handle(responseCompact),
		).
		AddRoute(
			router.NewRoute("/messages", http.MethodPost).
				Handle(message),
		).
		AddRoute(
			router.NewRoute("/embeddings", http.MethodPost).
				Handle(embedding),
		)

	// WebSocket route for /v1/responses (no RequireJSON middleware)
	router.NewGroupRouter("/v1").
		Use(middleware.APIKeyAuth()).
		AddRoute(
			router.NewRoute("/responses", http.MethodGet).
				Handle(wsResponse),
		)
}

func chat(c *gin.Context) {
	relay.TextHandler(llm.APIFormatOpenAIChatCompletion, c)
}
func response(c *gin.Context) {
	relay.TextHandler(llm.APIFormatOpenAIResponse, c)
}
func responseCompact(c *gin.Context) {
	relay.HandleResponsesCompact(c)
}
func message(c *gin.Context) {
	relay.TextHandler(llm.APIFormatAnthropicMessage, c)
}
func embedding(c *gin.Context) {
	relay.TextHandler(llm.APIFormatOpenAIEmbedding, c)
}
func wsResponse(c *gin.Context) {
	relay.HandleWSResponse(c)
}
