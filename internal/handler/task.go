package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/trae/midjourney-api/internal/service"
	"github.com/trae/midjourney-api/pkg/constants"
	"github.com/trae/midjourney-api/pkg/response"
)

type TaskHandler struct {
	taskService service.TaskService
}

func NewTaskHandler(taskService service.TaskService) *TaskHandler {
	return &TaskHandler{
		taskService: taskService,
	}
}

type CreateImagineTaskReq struct {
	Prompt      string `json:"prompt" binding:"required"`
	CallbackURL string `json:"callback_url"`
}

// CreateImagineTask creates an Imagine task
// @Summary Create Imagine task
// @Description Create a Midjourney image generation task based on the given prompt
// @Tags Task
// @Accept json
// @Produce json
// @Param request body CreateImagineTaskReq true "Task request"
// @Success 201 {object} response.Response "Created successfully"
// @Failure 400 {object} response.Response "Bad request"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/tasks/imagine [post]
func (h *TaskHandler) CreateImagineTask(c *gin.Context) {
	ctx := c.Request.Context()
	var req CreateImagineTaskReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	// Use fixed UserID = 1 for simplicity
	userID := constants.DefaultUserID

	resp, err := h.taskService.CreateImagineTask(ctx, &service.CreateTaskRequest{
		UserID:      userID,
		Prompt:      req.Prompt,
		CallbackURL: req.CallbackURL,
	})

	if err != nil {
		response.Error(c, err)
		return
	}

	response.Created(c, resp)
}

type CreateDescribeTaskReq struct {
	ImageURL    string `json:"image_url" binding:"required"`
	CallbackURL string `json:"callback_url"`
}

// CreateDescribeTask creates a Describe task
// @Summary Create Describe task
// @Description Create a Midjourney describe task that generates text prompts based on an image URL
// @Tags Task
// @Accept json
// @Produce json
// @Param request body CreateDescribeTaskReq true "Describe task request"
// @Success 201 {object} response.Response "Created successfully"
// @Failure 400 {object} response.Response "Bad request"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/tasks/describe [post]
func (h *TaskHandler) CreateDescribeTask(c *gin.Context) {
	ctx := c.Request.Context()
	var req CreateDescribeTaskReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	userID := constants.DefaultUserID

	resp, err := h.taskService.CreateDescribeTask(ctx, &service.CreateDescribeTaskRequest{
		UserID:      userID,
		ImageURL:    req.ImageURL,
		CallbackURL: req.CallbackURL,
	})
	if err != nil {
		response.Error(c, err)
		return
	}

	response.Created(c, resp)
}

// GetTask retrieves the task status by task ID
// @Summary Get task status
// @Description Get task details by task ID
// @Tags Task
// @Produce json
// @Param task_id path string true "Task ID"
// @Success 200 {object} response.Response "Success"
// @Failure 404 {object} response.Response "Task not found"
// @Router /api/v1/tasks/{task_id} [get]
func (h *TaskHandler) GetTask(c *gin.Context) {
	ctx := c.Request.Context()
	taskID := c.Param("task_id")

	task, err := h.taskService.GetTask(ctx, taskID)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.Success(c, task)
}

// ListTasks returns the task list
// @Summary List tasks
// @Description Get the task list
// @Tags Task
// @Produce json
// @Param limit query int false "Page size" default(10)
// @Param offset query int false "Offset" default(0)
// @Success 200 {object} response.Response "Success"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/tasks [get]
func (h *TaskHandler) ListTasks(c *gin.Context) {
	ctx := c.Request.Context()
	// Use fixed UserID = 1 for simplicity
	userID := constants.DefaultUserID

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

	tasks, total, err := h.taskService.ListTasks(ctx, userID, limit, offset)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.Success(c, gin.H{
		"tasks": tasks,
		"total": total,
	})
}

// GetQueueList retrieves the current task queue list
// @Summary Get queue list
// @Description Get the list of tasks currently waiting to be processed
// @Tags Task
// @Produce json
// @Success 200 {object} response.Response "Success"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/tasks/queue [get]
func (h *TaskHandler) GetQueueList(c *gin.Context) {
	ctx := c.Request.Context()
	queueTasks, err := h.taskService.GetQueueList(ctx)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.Success(c, queueTasks)
}

// PerformTaskAction performs an action on a completed task
// @Summary Perform task action
// @Description Perform an action on a completed task (upscale, zoom_out_2x, zoom_out_1_5x, upscale_subtle, upscale_creative)
// @Tags Task
// @Accept json
// @Produce json
// @Param request body service.TaskActionRequest true "Action request"
// @Success 201 {object} response.Response "Action submitted"
// @Failure 400 {object} response.Response "Bad request"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/tasks/action [post]
func (h *TaskHandler) PerformTaskAction(c *gin.Context) {
	ctx := c.Request.Context()
	var req service.TaskActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	resp, err := h.taskService.PerformTaskAction(ctx, &req)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.Created(c, resp)
}
