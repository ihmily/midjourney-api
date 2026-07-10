package handler

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/internal/service"
	"github.com/trae/midjourney-api/pkg/constants"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"github.com/trae/midjourney-api/pkg/redact"
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

type TaskListResponse struct {
	Tasks []TaskView `json:"tasks"`
	Total int64      `json:"total"`
}

type TaskView struct {
	TaskID       string           `json:"task_id"`
	ParentTaskID string           `json:"parent_task_id,omitempty"`
	Type         model.TaskType   `json:"type"`
	Prompt       string           `json:"prompt,omitempty"`
	Status       model.TaskStatus `json:"status"`
	Progress     int              `json:"progress"`
	ImageURL     string           `json:"image_url,omitempty"`
	OSSImageURL  string           `json:"oss_image_url,omitempty"`
	ErrorMessage string           `json:"error_message,omitempty"`
	Description  string           `json:"description,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
	FinishedAt   *time.Time       `json:"finished_at,omitempty"`
}

// CreateImagineTask creates an Imagine task
// @Summary Create Imagine task
// @Description Create a Midjourney image generation task based on the given prompt
// @Tags Task
// @Accept json
// @Produce json
// @Param request body CreateImagineTaskReq true "Task request"
// @Success 201 {object} response.Response{data=service.TaskResponse} "Created successfully"
// @Failure 400 {object} response.Response "Bad request"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/tasks/imagine [post]
func (h *TaskHandler) CreateImagineTask(c *gin.Context) {
	ctx := c.Request.Context()
	var req CreateImagineTaskReq
	if err := bindStrictJSON(c, &req); err != nil {
		response.Error(c, err)
		return
	}
	taskService, err := h.taskServiceOrError()
	if err != nil {
		response.Error(c, err)
		return
	}

	resp, err := taskService.CreateImagineTask(ctx, &service.CreateTaskRequest{
		Prompt:      req.Prompt,
		CallbackURL: req.CallbackURL,
	})

	if err != nil {
		response.Error(c, err)
		return
	}
	if resp == nil {
		response.Error(c, handlerInternalError("created imagine task result is required"))
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
// @Success 201 {object} response.Response{data=service.TaskResponse} "Created successfully"
// @Failure 400 {object} response.Response "Bad request"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/tasks/describe [post]
func (h *TaskHandler) CreateDescribeTask(c *gin.Context) {
	ctx := c.Request.Context()
	var req CreateDescribeTaskReq
	if err := bindStrictJSON(c, &req); err != nil {
		response.Error(c, err)
		return
	}
	taskService, err := h.taskServiceOrError()
	if err != nil {
		response.Error(c, err)
		return
	}

	resp, err := taskService.CreateDescribeTask(ctx, &service.CreateDescribeTaskRequest{
		ImageURL:    req.ImageURL,
		CallbackURL: req.CallbackURL,
	})
	if err != nil {
		response.Error(c, err)
		return
	}
	if resp == nil {
		response.Error(c, handlerInternalError("created describe task result is required"))
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
// @Success 200 {object} response.Response{data=TaskView} "Success"
// @Failure 404 {object} response.Response "Task not found"
// @Router /api/v1/tasks/{task_id} [get]
func (h *TaskHandler) GetTask(c *gin.Context) {
	ctx := c.Request.Context()
	taskID := c.Param("task_id")
	taskService, err := h.taskServiceOrError()
	if err != nil {
		response.Error(c, err)
		return
	}

	task, err := taskService.GetTask(ctx, taskID)
	if err != nil {
		response.Error(c, err)
		return
	}
	if task == nil {
		response.Error(c, handlerInternalError("task result is required"))
		return
	}

	response.Success(c, taskViewFromModel(task))
}

// ListTasks returns the task list
// @Summary List tasks
// @Description Get the task list
// @Tags Task
// @Produce json
// @Param limit query int false "Page size" default(10)
// @Param offset query int false "Offset" default(0)
// @Success 200 {object} response.Response{data=TaskListResponse} "Success"
// @Failure 400 {object} response.Response "Bad request"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/tasks [get]
func (h *TaskHandler) ListTasks(c *gin.Context) {
	ctx := c.Request.Context()

	limit, offset, err := parseListPagination(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	taskService, err := h.taskServiceOrError()
	if err != nil {
		response.Error(c, err)
		return
	}

	tasks, total, err := taskService.ListTasks(ctx, limit, offset)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.Success(c, TaskListResponse{
		Tasks: taskViewsFromModels(tasks),
		Total: total,
	})
}

func taskViewsFromModels(tasks []model.Task) []TaskView {
	views := make([]TaskView, 0, len(tasks))
	for i := range tasks {
		views = append(views, taskViewFromModel(&tasks[i]))
	}
	return views
}

func taskViewFromModel(task *model.Task) TaskView {
	if task == nil {
		return TaskView{}
	}

	return TaskView{
		TaskID:       task.TaskID,
		ParentTaskID: task.ParentTaskID,
		Type:         task.Type,
		Prompt:       task.Prompt,
		Status:       task.Status,
		Progress:     task.Progress,
		ImageURL:     task.ImageURL,
		OSSImageURL:  task.OSSImageURL,
		ErrorMessage: redact.Text(task.ErrorMessage),
		Description:  task.Description,
		CreatedAt:    task.CreatedAt,
		UpdatedAt:    task.UpdatedAt,
		FinishedAt:   task.FinishedAt,
	}
}

func parseListPagination(c *gin.Context) (int, int, error) {
	limit, err := parseOptionalIntQuery(c, "limit", constants.DefaultListLimit)
	if err != nil {
		return 0, 0, err
	}
	if limit <= 0 {
		return 0, 0, apperrors.NewInvalidInput("limit must be greater than 0")
	}
	if limit > constants.MaxListLimit {
		limit = constants.MaxListLimit
	}

	offset, err := parseOptionalIntQuery(c, "offset", 0)
	if err != nil {
		return 0, 0, err
	}
	if offset < 0 {
		return 0, 0, apperrors.NewInvalidInput("offset must be greater than or equal to 0")
	}

	return limit, offset, nil
}

func parseOptionalIntQuery(c *gin.Context, key string, defaultValue int) (int, error) {
	raw := c.Query(key)
	if raw == "" {
		return defaultValue, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, apperrors.NewInvalidInput(key + " must be an integer")
	}
	return value, nil
}

// GetQueueList retrieves the current task queue list
// @Summary Get queue list
// @Description Get the list of tasks currently waiting to be processed
// @Tags Task
// @Produce json
// @Success 200 {object} response.Response{data=service.QueueStatus} "Success"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/tasks/queue [get]
func (h *TaskHandler) GetQueueList(c *gin.Context) {
	ctx := c.Request.Context()
	taskService, err := h.taskServiceOrError()
	if err != nil {
		response.Error(c, err)
		return
	}
	queueTasks, err := taskService.GetQueueList(ctx)
	if err != nil {
		response.Error(c, err)
		return
	}
	if queueTasks == nil {
		response.Error(c, handlerInternalError("queue status result is required"))
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
// @Param request body PerformTaskActionReq true "Action request"
// @Success 201 {object} response.Response{data=service.TaskResponse} "Action submitted"
// @Failure 400 {object} response.Response "Bad request"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/tasks/action [post]
func (h *TaskHandler) PerformTaskAction(c *gin.Context) {
	ctx := c.Request.Context()
	var req PerformTaskActionReq
	if err := bindStrictJSON(c, &req); err != nil {
		response.Error(c, err)
		return
	}
	taskService, err := h.taskServiceOrError()
	if err != nil {
		response.Error(c, err)
		return
	}

	resp, err := taskService.PerformTaskAction(ctx, &service.TaskActionRequest{
		TaskID:      req.TaskID,
		ActionType:  req.ActionType,
		Index:       req.indexValue(),
		CallbackURL: req.CallbackURL,
	})
	if err != nil {
		response.Error(c, err)
		return
	}
	if resp == nil {
		response.Error(c, handlerInternalError("task action result is required"))
		return
	}

	response.Created(c, resp)
}

type PerformTaskActionReq struct {
	TaskID      string `json:"task_id" binding:"required"`                                   // Original task ID
	ActionType  string `json:"action_type" binding:"required"`                               // Operation type
	Index       *int   `json:"index" binding:"required" minimum:"1" maximum:"4" example:"1"` // Index: 1-4
	CallbackURL string `json:"callback_url"`
}

func (r PerformTaskActionReq) indexValue() int {
	if r.Index == nil {
		return 0
	}
	return *r.Index
}

func (h *TaskHandler) taskServiceOrError() (service.TaskService, error) {
	if h == nil || h.taskService == nil {
		return nil, handlerInternalError("task service is required")
	}
	return h.taskService, nil
}
