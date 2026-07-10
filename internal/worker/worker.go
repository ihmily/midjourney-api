package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/trae/midjourney-api/internal/service"
	"github.com/trae/midjourney-api/pkg/constants"
	"github.com/trae/midjourney-api/pkg/redact"
	"go.uber.org/zap"
)

type Worker struct {
	taskService service.TaskService
	redisClient *redis.Client
	queueName   string
	timeout     time.Duration
	logger      *zap.Logger
	ctx         context.Context
	cancel      context.CancelFunc
	stopChan    chan struct{}
	stopOnce    sync.Once
	done        chan struct{}
	doneOnce    sync.Once
	started     atomic.Bool
	running     atomic.Bool
}

func NewWorker(taskService service.TaskService, redisClient *redis.Client, queueName string, timeout time.Duration, logger *zap.Logger) *Worker {
	if logger == nil {
		logger = zap.NewNop()
	}
	if timeout <= 0 {
		timeout = constants.DefaultTaskTimeout
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Worker{
		taskService: taskService,
		redisClient: redisClient,
		queueName:   strings.TrimSpace(queueName),
		timeout:     timeout,
		logger:      logger,
		ctx:         ctx,
		cancel:      cancel,
		stopChan:    make(chan struct{}),
		done:        make(chan struct{}),
	}
}

func (w *Worker) start() {
	if w == nil {
		return
	}
	if w.logger == nil {
		w.logger = zap.NewNop()
	}
	if w.redisClient == nil {
		w.logger.Error("Worker cannot start: redis client is required")
		return
	}
	if w.taskService == nil {
		w.logger.Error("Worker cannot start: task service is required")
		return
	}
	if w.queueName == "" {
		w.logger.Error("Worker cannot start: queue name is required")
		return
	}

	w.logger.Info("Worker started")

	for {
		select {
		case <-w.ctx.Done():
			w.logger.Info("Worker stopped")
			return
		case <-w.stopChan:
			w.logger.Info("Worker stopped")
			return
		default:
			w.processTask()
		}
	}
}

func (w *Worker) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
		if w.stopChan != nil {
			close(w.stopChan)
		}
	})
}

func (w *Worker) Wait(timeout time.Duration) bool {
	if w == nil || !w.started.Load() {
		return true
	}
	done := w.done
	if done == nil {
		return !w.running.Load()
	}
	if timeout <= 0 {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (w *Worker) processTask() {
	result, err := w.redisClient.BRPop(w.ctx, constants.QueuePollTimeout, w.queueName).Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			w.logger.Warn("Failed to pop task from queue", redactedErrorField(err))
		}
		return
	}

	if len(result) < 2 {
		return
	}

	data := result[1]
	w.processQueueMessage(data)
}

func (w *Worker) processQueueMessage(data string) {
	ctx, cancel := context.WithTimeout(w.ctx, w.timeout)
	defer cancel()

	switch service.QueueMessageKindFromJSON(data) {
	case service.QueueMessageKindDescribe:
		w.processDescribeTask(ctx, data)
		return
	case service.QueueMessageKindImagine:
		w.processImagineTask(ctx, data)
		return
	case service.QueueMessageKindAction:
		w.processActionTask(ctx, data)
		return
	}

	w.logger.Error("Failed to parse task message: missing or unknown kind")
	w.rejectUnknownQueueMessage(data)
}

type rejectableQueueMessage struct {
	TaskID    string `json:"task_id"`
	AccountID uint   `json:"account_id"`
}

func parseRejectableQueueMessage(data string) (rejectableQueueMessage, bool) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(strings.NewReader(data)).Decode(&raw); err != nil {
		return rejectableQueueMessage{}, false
	}

	var msg rejectableQueueMessage
	msg.TaskID = parseRejectableString(raw["task_id"])
	msg.AccountID = parseRejectableUint(raw["account_id"])
	return msg, msg.TaskID != ""
}

func parseRejectableString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func parseRejectableUint(raw json.RawMessage) uint {
	if len(raw) == 0 {
		return 0
	}

	var number uint64
	if err := json.Unmarshal(raw, &number); err == nil {
		return uint(number)
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0
	}
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0
	}
	return uint(parsed)
}

func (w *Worker) rejectUnknownQueueMessage(data string) {
	w.rejectQueueMessage(data, fmt.Errorf("missing or unknown queue message kind"))
}

func (w *Worker) rejectInvalidQueueMessage(data string, reason error) {
	if reason == nil {
		reason = fmt.Errorf("invalid queue message")
	}
	w.rejectQueueMessage(data, reason)
}

func (w *Worker) rejectQueueMessage(data string, reason error) {
	if w.taskService == nil {
		return
	}

	msg, ok := parseRejectableQueueMessage(data)
	if !ok {
		return
	}

	if rejectErr := w.taskService.RejectQueueMessage(msg.TaskID, msg.AccountID, reason); rejectErr != nil {
		w.logger.Error("Failed to reject queue message",
			zap.String("task_id", msg.TaskID),
			zap.Uint("account_id", msg.AccountID),
			redactedErrorField(rejectErr))
	}
}

func (w *Worker) processDescribeTask(ctx context.Context, data string) {
	describeMsg, err := service.DecodeDescribeTaskMessage(data)
	if err != nil {
		w.logger.Error("Invalid describe task message", redactedErrorField(err))
		w.rejectInvalidQueueMessage(data, err)
		return
	}

	w.logger.Info("Processing describe task",
		zap.String("task_id", describeMsg.TaskID),
		zap.String("image_url", redact.URL(describeMsg.ImageURL)),
		zap.Uint("account_id", describeMsg.AccountID))

	if err := w.taskService.ProcessDescribeTask(ctx, &describeMsg); err != nil {
		w.logger.Error("Failed to process describe task",
			zap.String("task_id", describeMsg.TaskID),
			zap.String("error", redact.Text(err.Error())))
		return
	}

	w.logger.Info("Describe task processed successfully",
		zap.String("task_id", describeMsg.TaskID))
}

func (w *Worker) processImagineTask(ctx context.Context, data string) {
	taskMsg, err := service.DecodeImagineTaskMessage(data)
	if err != nil {
		w.logger.Error("Invalid imagine task message", redactedErrorField(err))
		w.rejectInvalidQueueMessage(data, err)
		return
	}

	w.logger.Info("Processing task",
		zap.String("task_id", taskMsg.TaskID),
		zap.Int("prompt_length", runeCount(taskMsg.Prompt)),
		zap.Uint("account_id", taskMsg.AccountID))

	if err := w.taskService.ProcessTask(ctx, &taskMsg); err != nil {
		w.logger.Error("Failed to process task",
			zap.String("task_id", taskMsg.TaskID),
			zap.Uint("account_id", taskMsg.AccountID),
			zap.String("error", redact.Text(err.Error())))
		return
	}

	w.logger.Info("Task processed successfully",
		zap.String("task_id", taskMsg.TaskID),
		zap.Uint("account_id", taskMsg.AccountID))
}

func (w *Worker) processActionTask(ctx context.Context, data string) {
	actionMsg, err := service.DecodeActionTaskMessage(data)
	if err != nil {
		w.logger.Error("Invalid action task message", redactedErrorField(err))
		w.rejectInvalidQueueMessage(data, err)
		return
	}

	w.logger.Info("Processing action task",
		zap.String("task_id", actionMsg.TaskID),
		zap.String("parent_task_id", actionMsg.ParentTaskID),
		zap.Uint("account_id", actionMsg.AccountID))

	if err := w.taskService.ProcessActionTask(ctx, &actionMsg); err != nil {
		w.logger.Error("Failed to process action task",
			zap.String("task_id", actionMsg.TaskID),
			zap.String("error", redact.Text(err.Error())))
		return
	}

	w.logger.Info("Action task processed successfully",
		zap.String("task_id", actionMsg.TaskID))
}

func runeCount(value string) int {
	return len([]rune(value))
}

func (w *Worker) Run() error {
	if w == nil {
		return nil
	}
	w.markStarted()
	defer func() {
		w.doneOnce.Do(func() {
			close(w.done)
		})
		w.running.Store(false)
	}()

	for {
		if panicked := w.startWithRecover(); !panicked {
			return nil
		}
		w.logger.Warn("Worker will restart in 1 second")
		select {
		case <-w.ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}

func (w *Worker) markStarted() {
	if w == nil {
		return
	}
	if w.done == nil {
		w.done = make(chan struct{})
	}
	w.started.Store(true)
	w.running.Store(true)
}

func (w *Worker) startWithRecover() (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error("Worker panic detected, preparing to restart", zap.String("panic", panicLogText(r)))
			panicked = true
		}
	}()
	w.start()
	return false
}

func panicLogText(value interface{}) string {
	return redact.Text(fmt.Sprint(value))
}

func redactedErrorField(err error) zap.Field {
	if err == nil {
		return zap.Skip()
	}
	return zap.String("error", redact.Text(err.Error()))
}

func StartWorkers(count int, taskService service.TaskService, redisClient *redis.Client, queueName string, timeout time.Duration, logger *zap.Logger) []*Worker {
	if logger == nil {
		logger = zap.NewNop()
	}
	if count <= 0 {
		return nil
	}
	if redisClient == nil {
		logger.Error("Workers cannot start: redis client is required")
		return nil
	}
	if taskService == nil {
		logger.Error("Workers cannot start: task service is required")
		return nil
	}
	queueName = strings.TrimSpace(queueName)
	if queueName == "" {
		logger.Error("Workers cannot start: queue name is required")
		return nil
	}

	workers := make([]*Worker, 0, count)
	for i := 0; i < count; i++ {
		w := NewWorker(taskService, redisClient, queueName, timeout, logger)
		workers = append(workers, w)
		w.markStarted()
		go func(id int, worker *Worker) {
			logger.Info(fmt.Sprintf("Worker #%d starting", id))
			_ = worker.Run()
		}(i+1, w)
	}
	return workers
}
