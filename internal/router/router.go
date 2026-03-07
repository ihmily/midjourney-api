package router

import (
	"net/http"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"github.com/trae/midjourney-api/internal/handler"
)

func Setup(r *gin.Engine, taskHandler *handler.TaskHandler, accountHandler *handler.AccountHandler, healthHandler *handler.HealthHandler) {
	// Swagger documentation
	r.GET("/swagger", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/swagger/index.html")
	})
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// Health check endpoints for monitoring
	r.GET("/live", healthHandler.LivenessCheck)

	// API v1
	v1 := r.Group("/api/v1")
	{
		// Task related
		tasks := v1.Group("/tasks")
		{
			tasks.POST("/imagine", taskHandler.CreateImagineTask)
			tasks.POST("/action", taskHandler.PerformTaskAction)
			tasks.GET("/:task_id", taskHandler.GetTask)
			tasks.GET("", taskHandler.ListTasks)
			tasks.GET("/queue", taskHandler.GetQueueList)
		}

		// Account related
		accounts := v1.Group("/accounts")
		{
			accounts.GET("", accountHandler.ListAccounts)
			accounts.GET("/:id/health", accountHandler.HealthCheckAccount)
			accounts.PUT("/:id/health", accountHandler.UpdateAccountHealth)
		}
	}
}
