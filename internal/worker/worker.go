package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/trae/midjourney-api/internal/service"
	"github.com/trae/midjourney-api/pkg/constants"
	"go.uber.org/zap"
)

type Worker struct {
	taskService service.TaskService
	redisClient *redis.Client
	queueName   string
	logger      *zap.Logger
	stopChan    chan struct{}
}

func NewWorker(taskService service.TaskService, redisClient *redis.Client, queueName string, logger *zap.Logger) *Worker {
	return &Worker{
		taskService: taskService,
		redisClient: redisClient,
		queueName:   queueName,
		logger:      logger,
		stopChan:    make(chan struct{}),
	}
}

func (w *Worker) Start() {
	w.logger.Info("Worker started")

	for {
		select {
		case <-w.stopChan:
			w.logger.Info("Worker stopped")
			return
		default:
			w.processTask()
		}
	}
}

func (w *Worker) Stop() {
	close(w.stopChan)
}

func (w *Worker) processTask() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	result, err := w.redisClient.BRPop(ctx, constants.QueuePollTimeout, w.queueName).Result()
	if err != nil {
		return
	}

	if len(result) < 2 {
		return
	}

	data := result[1]

	var taskMsg service.TaskMessage
	if err := json.Unmarshal([]byte(data), &taskMsg); err == nil && taskMsg.Prompt != "" {
		w.logger.Info("Processing task",
			zap.String("task_id", taskMsg.TaskID),
			zap.String("prompt", taskMsg.Prompt),
			zap.String("guild_id", taskMsg.GuildID),
			zap.String("channel_id", taskMsg.ChannelID),
			zap.String("user_token", "***masked***"))

		if err := w.taskService.ProcessTask(ctx, &taskMsg); err != nil {
			w.logger.Error("Failed to process task",
				zap.String("task_id", taskMsg.TaskID),
				zap.String("guild_id", taskMsg.GuildID),
				zap.String("channel_id", taskMsg.ChannelID),
				zap.Error(err))
			return
		}

		w.logger.Info("Task processed successfully",
			zap.String("task_id", taskMsg.TaskID),
			zap.String("guild_id", taskMsg.GuildID),
			zap.String("channel_id", taskMsg.ChannelID),
			zap.String("user_token", "***masked***"))
		return
	}

	var actionMsg service.TaskActionMessage
	if err := json.Unmarshal([]byte(data), &actionMsg); err == nil && actionMsg.CustomID != "" {
		w.logger.Info("Processing action task",
			zap.String("task_id", actionMsg.TaskID),
			zap.String("parent_task_id", actionMsg.ParentTaskID),
			zap.String("custom_id", actionMsg.CustomID),
			zap.String("guild_id", actionMsg.GuildID),
			zap.String("channel_id", actionMsg.ChannelID))

		if err := w.taskService.ProcessActionTask(ctx, &actionMsg); err != nil {
			w.logger.Error("Failed to process action task",
				zap.String("task_id", actionMsg.TaskID),
				zap.String("custom_id", actionMsg.CustomID),
				zap.Error(err))
			return
		}

		w.logger.Info("Action task processed successfully",
			zap.String("task_id", actionMsg.TaskID),
			zap.String("custom_id", actionMsg.CustomID))
		return
	}

	w.logger.Error("Failed to parse task message: unknown message format")
}

func (w *Worker) Run() error {
	for {
		if panicked := w.startWithRecover(); !panicked {
			return nil
		}
		w.logger.Warn("Worker will restart in 1 second")
		time.Sleep(time.Second)
	}
}

func (w *Worker) startWithRecover() (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error("Worker panic detected, preparing to restart", zap.Any("panic", r))
			panicked = true
		}
	}()
	w.Start()
	return false
}

func StartWorkers(count int, taskService service.TaskService, redisClient *redis.Client, queueName string, logger *zap.Logger) []*Worker {
	workers := make([]*Worker, 0, count)
	for i := 0; i < count; i++ {
		w := NewWorker(taskService, redisClient, queueName, logger)
		workers = append(workers, w)
		go func(id int) {
			logger.Info(fmt.Sprintf("Worker #%d starting", id))
			w.Run()
		}(i + 1)
	}
	return workers
}
