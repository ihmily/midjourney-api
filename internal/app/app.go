package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/trae/midjourney-api/internal/config"
	"github.com/trae/midjourney-api/internal/discord"
	"github.com/trae/midjourney-api/internal/handler"
	"github.com/trae/midjourney-api/internal/middleware"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/internal/oss"
	internalredis "github.com/trae/midjourney-api/internal/redis"
	"github.com/trae/midjourney-api/internal/repository"
	"github.com/trae/midjourney-api/internal/router"
	"github.com/trae/midjourney-api/internal/service"
	"github.com/trae/midjourney-api/internal/worker"
	"github.com/trae/midjourney-api/pkg/constants"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"github.com/trae/midjourney-api/pkg/logger"
	"github.com/trae/midjourney-api/pkg/redact"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type Application struct {
	Config *config.Config
	DB     *gorm.DB
	Redis  *redis.Client
	Logger *zap.Logger

	// Repositories
	TaskRepo    repository.TaskRepository
	AccountRepo repository.AccountRepository

	// Services
	TaskService    service.TaskService
	AccountService service.AccountService

	// OSS Uploader
	OSSUploader oss.Uploader

	// Discord Listener (one per account)
	Listeners map[uint]accountListener

	// Workers
	Workers []*worker.Worker

	// Handlers
	TaskHandler    *handler.TaskHandler
	AccountHandler *handler.AccountHandler
	HealthHandler  *handler.HealthHandler

	// HTTP Server
	HTTPServer *http.Server

	// Listeners mutex
	listenersMu         sync.RWMutex
	listenerGenerations map[uint]uint64
	listenersStopping   bool
	newAccountListener  accountListenerFactory
	listenerStartupWait time.Duration
	timeoutSweepCancel  context.CancelFunc
}

type accountListener interface {
	Start() error
	Stop() error
	GetBotInfo() (username string, userID string)
}

type accountListenerFactory func(botToken, midjourneyBotID string, db *gorm.DB, logger *zap.Logger, ossUploader oss.Uploader) accountListener

func newDiscordAccountListener(botToken, midjourneyBotID string, db *gorm.DB, logger *zap.Logger, ossUploader oss.Uploader) accountListener {
	return discord.NewListener(botToken, midjourneyBotID, db, logger, ossUploader)
}

func New(configPath string) (*Application, error) {
	app := &Application{
		Listeners:           make(map[uint]accountListener),
		listenerGenerations: make(map[uint]uint64),
		newAccountListener:  newDiscordAccountListener,
		listenerStartupWait: constants.ListenerStartupWait,
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	app.Config = cfg

	zapLogger, err := logger.Init(cfg.Server.Mode)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %w", err)
	}
	app.Logger = zapLogger

	db, err := app.initDatabase()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	app.DB = db
	app.Logger.Info("Database connection successful")

	redisClient, err := internalredis.Init(&cfg.Redis)
	if err != nil {
		app.closeResources()
		return nil, fmt.Errorf("failed to initialize redis: %w", err)
	}
	app.Redis = redisClient
	app.Logger.Info("Redis connection successful")

	app.TaskRepo = repository.NewTaskRepository(db)
	app.AccountRepo = repository.NewAccountRepository(db)

	app.AccountService = service.NewAccountService(app.AccountRepo, app.Logger)
	app.TaskService = service.NewTaskService(
		app.TaskRepo,
		app.AccountService,
		&cfg.Discord,
		redisClient,
		&cfg.Task,
		app.Logger,
	)

	ossUploader, err := oss.NewUploader(&cfg.OSS, app.Logger)
	if err != nil {
		app.closeResources()
		return nil, fmt.Errorf("failed to initialize OSS uploader: %w", err)
	}
	app.OSSUploader = ossUploader
	if ossUploader != nil {
		app.Logger.Info("OSS upload enabled", zap.String("provider", cfg.OSS.Provider))
	} else {
		app.Logger.Info("OSS upload disabled")
	}

	app.TaskHandler = handler.NewTaskHandler(app.TaskService)
	app.AccountHandler = handler.NewAccountHandler(app.AccountService, app)
	app.HealthHandler = handler.NewHealthHandler()

	return app, nil
}

func (app *Application) initDatabase() (*gorm.DB, error) {
	cfg := &app.Config.Database
	dsn := postgresDSN(cfg)

	logLevel := gormlogger.Silent
	switch cfg.LogLevel {
	case "error":
		logLevel = gormlogger.Error
	case "warn":
		logLevel = gormlogger.Warn
	case "info":
		logLevel = gormlogger.Info
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(logLevel),
	})
	if err != nil {
		return nil, databaseInitError(err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, databaseInitError(err)
	}

	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Hour)
	sqlDB.SetConnMaxIdleTime(10 * time.Minute)

	if err := db.AutoMigrate(
		&model.Account{},
		&model.Task{},
	); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	if err := app.cleanupDatabaseSchema(db); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	if err := app.reconcileAccountJobCounts(db); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	return db, nil
}

func databaseInitError(err error) error {
	return redact.Error(err)
}

func postgresDSN(cfg *config.DatabaseConfig) string {
	if cfg == nil {
		return ""
	}
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		postgresDSNValue(cfg.Host),
		cfg.Port,
		postgresDSNValue(cfg.User),
		postgresDSNValue(cfg.Password),
		postgresDSNValue(cfg.DBName),
		postgresDSNValue(cfg.SSLMode),
	)
}

func postgresDSNValue(value string) string {
	if value == "" || !strings.ContainsAny(value, " \t\r\n'\\") {
		return value
	}
	escaped := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value)
	return "'" + escaped + "'"
}

func (app *Application) cleanupDatabaseSchema(db *gorm.DB) error {
	if err := db.Exec(fmt.Sprintf(`
DO $$
BEGIN
	DROP TABLE IF EXISTS users;

	ALTER TABLE tasks DROP COLUMN IF EXISTS user_id;

	UPDATE tasks
	SET progress = 0
	WHERE progress IS NULL
		OR progress < 0;

	UPDATE tasks
	SET progress = 100
	WHERE progress > 100;

	ALTER TABLE tasks ALTER COLUMN progress SET DEFAULT 0;
	ALTER TABLE tasks ALTER COLUMN progress SET NOT NULL;

	UPDATE accounts
	SET is_disabled = FALSE
	WHERE is_disabled IS NULL;

	UPDATE accounts
	SET is_healthy = FALSE
	WHERE is_healthy IS NULL;

	UPDATE accounts
	SET concurrent_limit = %[1]d
	WHERE concurrent_limit IS NULL
		OR concurrent_limit <= 0;

	UPDATE accounts
	SET current_jobs = 0
	WHERE current_jobs IS NULL
		OR current_jobs < 0;

	UPDATE accounts
	SET error_count = 0
	WHERE error_count IS NULL
		OR error_count < 0;

	UPDATE accounts
	SET success_count = 0
	WHERE success_count IS NULL
		OR success_count < 0;

	ALTER TABLE accounts ALTER COLUMN is_disabled SET DEFAULT FALSE;
	ALTER TABLE accounts ALTER COLUMN is_disabled SET NOT NULL;
	ALTER TABLE accounts ALTER COLUMN is_healthy SET DEFAULT FALSE;
	ALTER TABLE accounts ALTER COLUMN is_healthy SET NOT NULL;
	ALTER TABLE accounts ALTER COLUMN concurrent_limit SET DEFAULT %[1]d;
	ALTER TABLE accounts ALTER COLUMN concurrent_limit SET NOT NULL;
	ALTER TABLE accounts ALTER COLUMN current_jobs SET DEFAULT 0;
	ALTER TABLE accounts ALTER COLUMN current_jobs SET NOT NULL;
	ALTER TABLE accounts ALTER COLUMN error_count SET DEFAULT 0;
	ALTER TABLE accounts ALTER COLUMN error_count SET NOT NULL;
	ALTER TABLE accounts ALTER COLUMN success_count SET DEFAULT 0;
	ALTER TABLE accounts ALTER COLUMN success_count SET NOT NULL;

	ALTER TABLE accounts DROP COLUMN IF EXISTS status;
	ALTER TABLE accounts DROP COLUMN IF EXISTS health;
	ALTER TABLE accounts DROP COLUMN IF EXISTS last_heartbeat;

	UPDATE accounts
	SET is_healthy = FALSE
	WHERE deleted_at IS NULL
		AND (
			is_disabled = TRUE
			OR TRIM(COALESCE(user_token, '')) = ''
		);

	IF NOT EXISTS (
		SELECT 1
		FROM pg_constraint
		WHERE conname = 'chk_accounts_concurrent_limit_positive'
			AND conrelid = 'accounts'::regclass
	) THEN
		ALTER TABLE accounts
			ADD CONSTRAINT chk_accounts_concurrent_limit_positive
			CHECK (concurrent_limit > 0);
	END IF;

	IF NOT EXISTS (
		SELECT 1
		FROM pg_constraint
		WHERE conname = 'chk_accounts_current_jobs_non_negative'
			AND conrelid = 'accounts'::regclass
	) THEN
		ALTER TABLE accounts
			ADD CONSTRAINT chk_accounts_current_jobs_non_negative
			CHECK (current_jobs >= 0);
	END IF;

	IF NOT EXISTS (
		SELECT 1
		FROM pg_constraint
		WHERE conname = 'chk_accounts_error_count_non_negative'
			AND conrelid = 'accounts'::regclass
	) THEN
		ALTER TABLE accounts
			ADD CONSTRAINT chk_accounts_error_count_non_negative
			CHECK (error_count >= 0);
	END IF;

	IF NOT EXISTS (
		SELECT 1
		FROM pg_constraint
		WHERE conname = 'chk_accounts_success_count_non_negative'
			AND conrelid = 'accounts'::regclass
	) THEN
		ALTER TABLE accounts
			ADD CONSTRAINT chk_accounts_success_count_non_negative
			CHECK (success_count >= 0);
	END IF;

	IF NOT EXISTS (
		SELECT 1
		FROM pg_constraint
		WHERE conname = 'chk_tasks_progress_range'
			AND conrelid = 'tasks'::regclass
	) THEN
		ALTER TABLE tasks
			ADD CONSTRAINT chk_tasks_progress_range
			CHECK (progress BETWEEN 0 AND 100);
	END IF;
END $$;
`, constants.DefaultConcurrentLimit)).Error; err != nil {
		return err
	}

	var duplicates []struct {
		GuildID   string
		ChannelID string
		Count     int64
	}
	if err := db.Raw(`
SELECT guild_id, channel_id, COUNT(*) AS count
FROM accounts
WHERE deleted_at IS NULL
GROUP BY guild_id, channel_id
HAVING COUNT(*) > 1
`).Scan(&duplicates).Error; err != nil {
		return err
	}
	if len(duplicates) > 0 {
		first := duplicates[0]
		return fmt.Errorf(
			"duplicate non-deleted accounts found for guild_id=%s channel_id=%s count=%d; delete duplicates before startup",
			first.GuildID,
			first.ChannelID,
			first.Count,
		)
	}

	return db.Exec(fmt.Sprintf(`
CREATE UNIQUE INDEX IF NOT EXISTS %s
ON accounts (guild_id, channel_id)
WHERE deleted_at IS NULL
`, repository.AccountActiveGuildChannelIndexName)).Error
}

func (app *Application) reconcileAccountJobCounts(db *gorm.DB) error {
	return db.Exec(`
UPDATE accounts
SET current_jobs = COALESCE((
	SELECT COUNT(*)
	FROM tasks
	WHERE tasks.account_id = accounts.id
		AND tasks.deleted_at IS NULL
		AND tasks.status IN ?
), 0)
WHERE accounts.deleted_at IS NULL;
`, model.ActiveTaskStatuses()).Error
}

func (app *Application) initListeners() {
	ctx, cancel := accountListenerOperationContext()
	defer cancel()

	accounts, err := app.AccountRepo.List(ctx)
	if err != nil {
		app.Logger.Error("Failed to get account list", zap.Error(err))
		return
	}

	for i := range accounts {
		account := accounts[i]
		if err := app.StartAccountListener(&account); err != nil {
			app.Logger.Warn("Failed to start account listener",
				zap.Uint("account_id", account.ID),
				zap.String("guild_id", account.GuildID),
				zap.Error(err))
		}
	}
}

func (app *Application) startAccountListenersAsync() {
	if app == nil {
		return
	}
	go app.initListeners()
}

// StartAccountListener starts a Discord listener for the given account.
// Implements handler.ListenerManager interface.
func (app *Application) StartAccountListener(account *model.Account) error {
	log := app.logger()

	if account == nil {
		return fmt.Errorf("account is nil")
	}

	if account.IsDisabled {
		return app.StopAccountListener(account.ID)
	}

	current, err := app.accountListenerCurrentWithTimeout(account)
	if err != nil {
		return err
	}
	if !current {
		log.Info("Account changed before listener start, skipping stale listener",
			zap.Uint("account_id", account.ID))
		return nil
	}

	if account.UserToken == "" {
		err := fmt.Errorf("account missing user_token")
		app.setAccountHealthWithTimeout(account.ID, false, err.Error())
		return err
	}

	oldListener, previousGeneration, generation, err := app.prepareAccountListenerStart(account.ID)
	if err != nil {
		return err
	}

	current, err = app.accountListenerGenerationAndConfigCurrentWithTimeout(account, generation)
	if err != nil {
		app.restoreOrStopPreparedAccountListener(account.ID, previousGeneration, generation, oldListener)
		return err
	}
	if !current {
		log.Info("Account changed before listener start, skipping stale listener",
			zap.Uint("account_id", account.ID))
		app.restoreOrStopPreparedAccountListener(account.ID, previousGeneration, generation, oldListener)
		return nil
	}

	if err := stopAccountListenerInstance(oldListener); err != nil {
		log.Warn("Failed to stop existing listener before restart",
			zap.Uint("account_id", account.ID),
			zap.Error(err))
	}

	listener := app.listenerFactory()(account.UserToken, app.discordApplicationID(), app.DB, log, app.OSSUploader)
	if listener == nil {
		err := fmt.Errorf("failed to create Discord listener")
		if app.accountListenerGenerationCurrent(account.ID, generation) {
			app.setAccountHealthWithTimeout(account.ID, false, err.Error())
		}
		return err
	}

	if err := listener.Start(); err != nil {
		wrappedErr := fmt.Errorf("failed to start Discord listener: %w", err)
		if app.accountListenerGenerationCurrent(account.ID, generation) {
			app.setAccountHealthWithTimeout(account.ID, false, wrappedErr.Error())
		}
		return wrappedErr
	}

	current, err = app.accountListenerGenerationAndConfigCurrentWithTimeout(account, generation)
	if err != nil {
		_ = stopAccountListenerInstance(listener)
		return err
	}
	if !current {
		log.Info("Account changed before listener registration, discarding stale listener",
			zap.Uint("account_id", account.ID))
		_ = stopAccountListenerInstance(listener)
		return nil
	}

	if !app.registerAccountListener(account.ID, generation, listener) {
		log.Info("Account listener start was superseded before registration",
			zap.Uint("account_id", account.ID))
		_ = stopAccountListenerInstance(listener)
		return nil
	}

	if wait := app.listenerStartupDelay(); wait > 0 {
		time.Sleep(wait)
	}

	current, err = app.accountListenerGenerationAndConfigCurrentWithTimeout(account, generation)
	if err != nil {
		_ = app.stopRegisteredAccountListener(account.ID, generation, listener)
		return err
	}
	if !current {
		log.Info("Account changed during listener startup, stopping stale listener",
			zap.Uint("account_id", account.ID))
		_ = app.stopRegisteredAccountListener(account.ID, generation, listener)
		return nil
	}

	username, userID := listener.GetBotInfo()
	if username != "" && userID != "" {
		if current, err = app.accountListenerGenerationAndConfigCurrentWithTimeout(account, generation); err != nil {
			_ = app.stopRegisteredAccountListener(account.ID, generation, listener)
			return err
		}
		if !current {
			log.Info("Account changed before listener health update, stopping stale listener",
				zap.Uint("account_id", account.ID))
			_ = app.stopRegisteredAccountListener(account.ID, generation, listener)
			return nil
		}

		log.Info("Bot connected",
			zap.Uint("account_id", account.ID),
			zap.String("username", username),
			zap.String("bot_user_id", userID))
		app.setAccountHealthWithTimeout(account.ID, true, "")
	} else {
		if app.accountListenerGenerationCurrent(account.ID, generation) {
			log.Warn("Listener started but failed to get bot info", zap.Uint("account_id", account.ID))
			app.setAccountHealthWithTimeout(account.ID, false, "Failed to get bot info")
		}
	}

	return nil
}

func (app *Application) listenerFactory() accountListenerFactory {
	if app != nil && app.newAccountListener != nil {
		return app.newAccountListener
	}
	return newDiscordAccountListener
}

func (app *Application) discordApplicationID() string {
	if app == nil || app.Config == nil {
		return ""
	}
	return app.Config.Discord.ApplicationID
}

func (app *Application) listenerStartupDelay() time.Duration {
	if app == nil {
		return 0
	}
	return app.listenerStartupWait
}

func (app *Application) accountListenerCurrent(ctx context.Context, account *model.Account) (bool, error) {
	if app == nil || app.AccountService == nil {
		return false, fmt.Errorf("account service is required")
	}
	if account == nil {
		return false, fmt.Errorf("account is required")
	}

	latest, err := app.AccountService.GetAccountByID(ctx, account.ID)
	if err != nil {
		var appErr *apperrors.AppError
		if errors.As(err, &appErr) && appErr.Code == apperrors.ErrCodeAccountNotFound {
			return false, nil
		}
		return false, err
	}

	return accountMatchesListenerConfig(account, latest), nil
}

func (app *Application) accountListenerCurrentWithTimeout(account *model.Account) (bool, error) {
	ctx, cancel := accountListenerOperationContext()
	defer cancel()
	return app.accountListenerCurrent(ctx, account)
}

func (app *Application) accountListenerGenerationAndConfigCurrent(ctx context.Context, account *model.Account, generation uint64) (bool, error) {
	if !app.accountListenerGenerationCurrent(account.ID, generation) {
		return false, nil
	}
	current, err := app.accountListenerCurrent(ctx, account)
	if err != nil || !current {
		return current, err
	}
	return app.accountListenerGenerationCurrent(account.ID, generation), nil
}

func (app *Application) accountListenerGenerationAndConfigCurrentWithTimeout(account *model.Account, generation uint64) (bool, error) {
	ctx, cancel := accountListenerOperationContext()
	defer cancel()
	return app.accountListenerGenerationAndConfigCurrent(ctx, account, generation)
}

func accountMatchesListenerConfig(expected, current *model.Account) bool {
	if expected == nil || current == nil {
		return false
	}
	return !current.IsDisabled &&
		current.UserToken == expected.UserToken &&
		current.GuildID == expected.GuildID &&
		current.ChannelID == expected.ChannelID
}

func (app *Application) setAccountHealth(ctx context.Context, accountID uint, isHealthy bool, lastError string) {
	if app == nil || app.AccountService == nil {
		app.logger().Error("Failed to update account health: account service is required",
			zap.Uint("account_id", accountID),
			zap.Bool("is_healthy", isHealthy))
		return
	}

	if err := app.AccountService.SetAccountHealthy(ctx, accountID, isHealthy, lastError); err != nil {
		app.logger().Error("Failed to update account health",
			zap.Uint("account_id", accountID),
			zap.Bool("is_healthy", isHealthy),
			zap.Error(err))
	}
}

func (app *Application) setAccountHealthWithTimeout(accountID uint, isHealthy bool, lastError string) {
	ctx, cancel := accountListenerOperationContext()
	defer cancel()
	app.setAccountHealth(ctx, accountID, isHealthy, lastError)
}

func accountListenerOperationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), constants.ListenerOperationTimeout)
}

// StopAccountListener stops and removes the Discord listener for the given account ID.
// Implements handler.ListenerManager interface.
func (app *Application) StopAccountListener(accountID uint) error {
	return stopAccountListenerInstance(app.removeAccountListener(accountID))
}

func (app *Application) prepareAccountListenerStart(accountID uint) (accountListener, uint64, uint64, error) {
	app.listenersMu.Lock()
	defer app.listenersMu.Unlock()

	if app.listenersStopping {
		return nil, 0, 0, fmt.Errorf("account listeners are shutting down")
	}
	app.ensureListenerStateLocked()

	previousGeneration := app.listenerGenerations[accountID]
	generation := previousGeneration + 1
	app.listenerGenerations[accountID] = generation
	listener := app.Listeners[accountID]
	delete(app.Listeners, accountID)

	return listener, previousGeneration, generation, nil
}

func (app *Application) registerAccountListener(accountID uint, generation uint64, listener accountListener) bool {
	app.listenersMu.Lock()
	defer app.listenersMu.Unlock()

	app.ensureListenerStateLocked()
	if app.listenersStopping || app.listenerGenerations[accountID] != generation {
		return false
	}

	app.Listeners[accountID] = listener
	return true
}

func (app *Application) accountListenerGenerationCurrent(accountID uint, generation uint64) bool {
	app.listenersMu.RLock()
	defer app.listenersMu.RUnlock()

	if app.listenersStopping || app.listenerGenerations == nil {
		return false
	}
	return app.listenerGenerations[accountID] == generation
}

func (app *Application) removeAccountListener(accountID uint) accountListener {
	app.listenersMu.Lock()
	defer app.listenersMu.Unlock()

	app.ensureListenerStateLocked()
	app.listenerGenerations[accountID]++
	listener := app.Listeners[accountID]
	delete(app.Listeners, accountID)
	return listener
}

func (app *Application) restoreOrStopPreparedAccountListener(accountID uint, previousGeneration, generation uint64, listener accountListener) {
	if app.restorePreparedAccountListener(accountID, previousGeneration, generation, listener) {
		return
	}
	if err := stopAccountListenerInstance(listener); err != nil {
		app.logger().Warn("Failed to stop superseded listener",
			zap.Uint("account_id", accountID),
			zap.Error(err))
	}
}

func (app *Application) restorePreparedAccountListener(accountID uint, previousGeneration, generation uint64, listener accountListener) bool {
	app.listenersMu.Lock()
	defer app.listenersMu.Unlock()

	app.ensureListenerStateLocked()
	if app.listenersStopping || app.listenerGenerations[accountID] != generation {
		return false
	}
	if listener == nil {
		return false
	}

	app.listenerGenerations[accountID] = previousGeneration
	app.Listeners[accountID] = listener
	return true
}

func (app *Application) stopRegisteredAccountListener(accountID uint, generation uint64, listener accountListener) error {
	app.listenersMu.Lock()
	if app.listenerGenerations == nil || app.Listeners == nil {
		app.listenersMu.Unlock()
		return nil
	}
	if app.listenerGenerations[accountID] != generation || app.Listeners[accountID] != listener {
		app.listenersMu.Unlock()
		return nil
	}
	app.listenerGenerations[accountID]++
	delete(app.Listeners, accountID)
	app.listenersMu.Unlock()

	return stopAccountListenerInstance(listener)
}

func (app *Application) ensureListenerStateLocked() {
	if app.Listeners == nil {
		app.Listeners = make(map[uint]accountListener)
	}
	if app.listenerGenerations == nil {
		app.listenerGenerations = make(map[uint]uint64)
	}
}

func stopAccountListenerInstance(listener accountListener) error {
	if listener == nil {
		return nil
	}
	return listener.Stop()
}

func (app *Application) setupRouter() *gin.Engine {
	gin.SetMode(app.Config.Server.Mode)

	r := gin.New()

	r.Use(middleware.Recovery(app.Logger))
	r.Use(middleware.RequestLogger(app.Logger))
	r.Use(middleware.RequestBodyLimit(0))

	// Configure CORS
	r.Use(cors.New(corsConfig()))

	// Set up routes
	router.Setup(r, app.TaskHandler, app.AccountHandler, app.HealthHandler)

	return r
}

func corsConfig() cors.Config {
	return cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: false,
	}
}

func (app *Application) Run() error {
	app.startAccountListenersAsync()
	defer app.closeResources()
	defer app.stopAllAccountListeners()

	// 1. Start Worker processes
	taskTimeout := time.Duration(app.Config.Task.Timeout) * time.Second
	if app.Config.Task.WorkerCount > 0 {
		app.Logger.Info(fmt.Sprintf("Starting %d Worker processes", app.Config.Task.WorkerCount))
		app.Workers = worker.StartWorkers(
			app.Config.Task.WorkerCount,
			app.TaskService,
			app.Redis,
			app.Config.Task.QueueName,
			taskTimeout,
			app.Logger,
		)
	}
	defer app.stopWorkers()

	app.startTaskTimeoutSweeper(taskTimeout)
	defer app.stopTaskTimeoutSweeper()

	// 2. Set up routes
	r := app.setupRouter()

	// 3. Create HTTP Server
	addr := fmt.Sprintf(":%d", app.Config.Server.Port)
	app.HTTPServer = newHTTPServer(addr, r)

	shutdownStop := make(chan struct{})
	shutdownStarted := make(chan struct{})
	shutdownDone := make(chan struct{})
	go app.gracefulShutdown(shutdownStop, shutdownStarted, shutdownDone)
	defer func() {
		close(shutdownStop)
		<-shutdownDone
	}()

	app.Logger.Info(fmt.Sprintf("Server is starting on %s", addr))
	if err := app.HTTPServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("failed to start server: %w", err)
	}

	select {
	case <-shutdownStarted:
		<-shutdownDone
	default:
	}

	return nil
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadTimeout:       constants.ServerReadTimeout,
		ReadHeaderTimeout: constants.ServerReadHeaderTimeout,
		WriteTimeout:      constants.ServerWriteTimeout,
		IdleTimeout:       constants.ServerIdleTimeout,
		MaxHeaderBytes:    constants.ServerMaxHeaderBytes,
	}
}

func (app *Application) gracefulShutdown(stop <-chan struct{}, started chan<- struct{}, done chan<- struct{}) {
	defer close(done)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	select {
	case <-quit:
		close(started)
	case <-stop:
		return
	}

	app.logger().Info("Server is shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	app.shutdownRuntime(ctx)

	app.logger().Info("Server has shutdown")
	_ = app.logger().Sync()
}

func (app *Application) shutdownRuntime(ctx context.Context) {
	if app == nil {
		return
	}

	app.shutdownHTTPServer(ctx)

	app.stopTaskTimeoutSweeper()
	app.stopWorkers()
	app.stopAllAccountListeners()
	app.closeResources()
}

func (app *Application) shutdownHTTPServer(ctx context.Context) {
	if app == nil || app.HTTPServer == nil {
		return
	}
	if err := app.HTTPServer.Shutdown(ctx); err != nil {
		app.logger().Error("Server forced to shutdown", zap.Error(err))
	}
}

func (app *Application) stopAllAccountListeners() {
	if app == nil {
		return
	}

	app.listenersMu.Lock()
	app.ensureListenerStateLocked()
	app.listenersStopping = true
	listeners := make(map[uint]accountListener, len(app.Listeners))
	for accountID := range app.listenerGenerations {
		app.listenerGenerations[accountID]++
	}
	for accountID, listener := range app.Listeners {
		listeners[accountID] = listener
		if _, ok := app.listenerGenerations[accountID]; !ok {
			app.listenerGenerations[accountID] = 1
		}
	}
	app.Listeners = make(map[uint]accountListener)
	app.listenersMu.Unlock()

	for accountID, listener := range listeners {
		if err := stopAccountListenerInstance(listener); err != nil {
			app.logger().Error("Failed to stop listener", zap.Uint("id", accountID), zap.Error(err))
		}
	}
}

func (app *Application) closeResources() {
	if app == nil {
		return
	}

	if app.DB != nil {
		if sqlDB, err := app.DB.DB(); err == nil {
			if err := sqlDB.Close(); err != nil {
				app.logger().Warn("Failed to close database", zap.Error(err))
			}
		}
		app.DB = nil
	}

	if app.Redis != nil {
		if err := app.Redis.Close(); err != nil {
			app.logger().Warn("Failed to close redis", zap.Error(err))
		}
		app.Redis = nil
	}
}

func (app *Application) logger() *zap.Logger {
	if app == nil || app.Logger == nil {
		return zap.NewNop()
	}
	return app.Logger
}

func (app *Application) stopWorkers() {
	for _, w := range app.Workers {
		if w != nil {
			w.Stop()
		}
	}

	deadline := time.Now().Add(constants.WorkerStopTimeout)
	for _, w := range app.Workers {
		if w != nil {
			remaining := time.Until(deadline)
			if remaining <= 0 || !w.Wait(remaining) {
				app.logger().Warn("Worker did not stop before timeout")
			}
		}
	}
}

func (app *Application) startTaskTimeoutSweeper(timeout time.Duration) {
	app.stopTaskTimeoutSweeper()

	ctx, cancel := context.WithCancel(context.Background())
	app.timeoutSweepCancel = cancel

	go app.runTaskTimeoutSweeper(ctx, timeout, taskTimeoutSweepInterval(timeout))
}

func (app *Application) stopTaskTimeoutSweeper() {
	if app == nil || app.timeoutSweepCancel == nil {
		return
	}
	app.timeoutSweepCancel()
	app.timeoutSweepCancel = nil
}

func (app *Application) runTaskTimeoutSweeper(ctx context.Context, timeout, interval time.Duration) {
	app.sweepTaskTimeouts(ctx, timeout)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			app.sweepTaskTimeouts(ctx, timeout)
		}
	}
}

func (app *Application) sweepTaskTimeouts(parent context.Context, timeout time.Duration) {
	if app == nil || app.TaskService == nil {
		return
	}

	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	count, err := app.TaskService.SweepTimedOutTasks(ctx, time.Now().Add(-timeout), constants.TimeoutSweepBatchSize)
	if err != nil {
		app.logger().Error("Failed to sweep timed out tasks", zap.Error(err))
		return
	}
	if count > 0 {
		app.logger().Warn("Timed out stale active tasks", zap.Int("count", count))
	}
}

func taskTimeoutSweepInterval(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return time.Minute
	}

	interval := timeout / 2
	if interval < time.Second {
		return time.Second
	}
	if interval > time.Minute {
		return time.Minute
	}
	return interval
}
