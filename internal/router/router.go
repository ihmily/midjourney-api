package router

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"github.com/trae/midjourney-api/docs"
	"github.com/trae/midjourney-api/internal/handler"
)

func Setup(r *gin.Engine, taskHandler *handler.TaskHandler, accountHandler *handler.AccountHandler, healthHandler *handler.HealthHandler) {
	// Swagger documentation
	r.GET("/swagger", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/swagger/index.html")
	})
	swaggerHandler := ginSwagger.WrapHandler(swaggerFiles.Handler)
	r.GET("/swagger/*any", func(c *gin.Context) {
		if c.Param("any") == "/doc.json" {
			serveSwaggerDoc(c)
			return
		}
		swaggerHandler(c)
	})

	// Health check endpoints for monitoring
	r.GET("/live", healthHandler.LivenessCheck)

	// API v1
	v1 := r.Group("/api/v1")
	{
		// Task related
		tasks := v1.Group("/tasks")
		{
			tasks.POST("/imagine", taskHandler.CreateImagineTask)
			tasks.POST("/describe", taskHandler.CreateDescribeTask)
			tasks.POST("/action", taskHandler.PerformTaskAction)
			tasks.GET("", taskHandler.ListTasks)
			tasks.GET("/queue", taskHandler.GetQueueList)
			tasks.GET("/:task_id", taskHandler.GetTask)
		}

		// Account related
		accounts := v1.Group("/accounts")
		{
			accounts.POST("", accountHandler.CreateAccount)
			accounts.GET("", accountHandler.ListAccounts)
			accounts.POST("/:id/restart", accountHandler.RestartAccount)
			accounts.PUT("/:id", accountHandler.UpdateAccount)
			accounts.DELETE("/:id", accountHandler.DeleteAccount)
		}
	}
}

func serveSwaggerDoc(c *gin.Context) {
	var spec map[string]any
	if err := json.Unmarshal([]byte(docs.SwaggerInfo.ReadDoc()), &spec); err != nil {
		c.String(http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError))
		return
	}

	if host := swaggerRequestHost(c.Request); host != "" {
		spec["host"] = host
	}
	spec["schemes"] = []string{swaggerRequestScheme(c.Request)}

	payload, err := json.Marshal(spec)
	if err != nil {
		c.String(http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError))
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
}

func swaggerRequestHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	if host := firstHeaderValue(r.Header.Get("X-Forwarded-Host")); host != "" {
		return host
	}
	if host := forwardedHeaderParam(r.Header.Get("Forwarded"), "host"); host != "" {
		return host
	}
	return strings.TrimSpace(r.Host)
}

func swaggerRequestScheme(r *http.Request) string {
	if r == nil {
		return "http"
	}
	if scheme := allowedSwaggerScheme(firstHeaderValue(r.Header.Get("X-Forwarded-Proto"))); scheme != "" {
		return scheme
	}
	if scheme := allowedSwaggerScheme(forwardedHeaderParam(r.Header.Get("Forwarded"), "proto")); scheme != "" {
		return scheme
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func firstHeaderValue(value string) string {
	value, _, _ = strings.Cut(value, ",")
	return strings.TrimSpace(value)
}

func forwardedHeaderParam(value, key string) string {
	value = firstHeaderValue(value)
	for _, part := range strings.Split(value, ";") {
		name, paramValue, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || !strings.EqualFold(name, key) {
			continue
		}
		return strings.Trim(strings.TrimSpace(paramValue), `"`)
	}
	return ""
}

func allowedSwaggerScheme(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "http" || value == "https" {
		return value
	}
	return ""
}
