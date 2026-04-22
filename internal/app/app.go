package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
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
	"github.com/trae/midjourney-api/pkg/logger"
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
	Listeners map[uint]*discord.Listener

	// Workers
	Workers []*worker.Worker

	// Handlers
	TaskHandler    *handler.TaskHandler
	AccountHandler *handler.AccountHandler
	HealthHandler  *handler.HealthHandler

	// HTTP Server
	HTTPServer *http.Server

	// Listeners mutex
	listenersMu sync.RWMutex
}

func New(configPath string) (*Application, error) {
	app := &Application{}

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

	go app.initListeners()

	return app, nil
}

func (app *Application) initDatabase() (*gorm.DB, error) {
	cfg := &app.Config.Database
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.SSLMode)

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
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}

	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Hour)
	sqlDB.SetConnMaxIdleTime(10 * time.Minute)

	if err := db.AutoMigrate(
		&model.User{},
		&model.Account{},
		&model.Task{},
	); err != nil {
		return nil, err
	}

	return db, nil
}

func (app *Application) initListeners() {
	ctx := context.Background()

	accounts, err := app.AccountRepo.List(ctx)
	if err != nil {
		app.Logger.Error("Failed to get account list", zap.Error(err))
		return
	}

	app.listenersMu.Lock()
	app.Listeners = make(map[uint]*discord.Listener)
	app.listenersMu.Unlock()

	for _, acc := range accounts {
		if acc.UserToken == "" {
			app.Logger.Warn("Account missing UserToken, skipping listener",
				zap.Uint("id", acc.ID),
				zap.String("guild_id", acc.GuildID))
			continue
		}

		listener := discord.NewListener(acc.UserToken, app.Config.Discord.ApplicationID, app.DB, app.Logger, app.OSSUploader)
		if listener == nil {
			app.Logger.Error("Failed to create listener", zap.Uint("id", acc.ID))
			app.AccountService.UpdateAccountHealth(ctx, acc.ID, model.AccountHealthUnhealthy, "Failed to create listener")
			continue
		}

		if err := listener.Start(); err != nil {
			app.Logger.Error("Failed to start listener", zap.Uint("id", acc.ID), zap.Error(err))
			app.AccountService.UpdateAccountHealth(ctx, acc.ID, model.AccountHealthUnhealthy, "Failed to start listener: "+err.Error())
			continue
		}

		app.listenersMu.Lock()
		app.Listeners[acc.ID] = listener
		app.listenersMu.Unlock()
		app.Logger.Info("Listener started", zap.Uint("id", acc.ID), zap.String("guild_id", acc.GuildID))
	}

	app.listenersMu.RLock()
	listenerCount := len(app.Listeners)
	app.listenersMu.RUnlock()

	if listenerCount > 0 {
		time.Sleep(constants.ListenerStartupWait)
	}

	app.listenersMu.RLock()
	defer app.listenersMu.RUnlock()
	for accountID, listener := range app.Listeners {
		username, userID := listener.GetBotInfo()
		if username != "" && userID != "" {
			if err := app.AccountService.UpdateAccountHealth(ctx, accountID, model.AccountHealthHealthy, ""); err != nil {
				app.Logger.Error("Failed to update account health", zap.Uint("id", accountID), zap.Error(err))
			}
			app.Logger.Info("Bot connected", zap.String("username", username), zap.String("user_id", userID))
		} else {
			app.Logger.Warn("Listener connected but failed to get bot info", zap.Uint("id", accountID))
			app.AccountService.UpdateAccountHealth(ctx, accountID, model.AccountHealthUnhealthy, "Failed to get bot info")
		}
	}
}

// StartAccountListener starts a Discord listener for the given account.
// Implements handler.ListenerManager interface.
func (app *Application) StartAccountListener(account *model.Account) error {
	if account.UserToken == "" {
		return fmt.Errorf("account missing user_token")
	}

	listener := discord.NewListener(account.UserToken, app.Config.Discord.ApplicationID, app.DB, app.Logger, app.OSSUploader)
	if listener == nil {
		return fmt.Errorf("failed to create Discord listener")
	}

	if err := listener.Start(); err != nil {
		return fmt.Errorf("failed to start Discord listener: %w", err)
	}

	app.listenersMu.Lock()
	if app.Listeners == nil {
		app.Listeners = make(map[uint]*discord.Listener)
	}
	app.Listeners[account.ID] = listener
	app.listenersMu.Unlock()

	// Wait briefly then check health
	time.Sleep(constants.ListenerStartupWait)
	ctx := context.Background()
	username, userID := listener.GetBotInfo()
	if username != "" && userID != "" {
		app.Logger.Info("Bot connected",
			zap.Uint("account_id", account.ID),
			zap.String("username", username),
			zap.String("user_id", userID))
		app.AccountService.UpdateAccountHealth(ctx, account.ID, model.AccountHealthHealthy, "")
	} else {
		app.Logger.Warn("Listener started but failed to get bot info", zap.Uint("account_id", account.ID))
		app.AccountService.UpdateAccountHealth(ctx, account.ID, model.AccountHealthUnhealthy, "Failed to get bot info")
	}

	return nil
}

// StopAccountListener stops and removes the Discord listener for the given account ID.
// Implements handler.ListenerManager interface.
func (app *Application) StopAccountListener(accountID uint) error {
	app.listenersMu.Lock()
	defer app.listenersMu.Unlock()

	listener, ok := app.Listeners[accountID]
	if !ok {
		return nil
	}

	err := listener.Stop()
	delete(app.Listeners, accountID)
	return err
}

func (app *Application) setupRouter() *gin.Engine {
	gin.SetMode(app.Config.Server.Mode)

	r := gin.New()

	r.Use(middleware.Recovery(app.Logger))
	r.Use(middleware.RequestLogger(app.Logger))

	// Configure CORS
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
	}))

	// Set up routes
	router.Setup(r, app.TaskHandler, app.AccountHandler, app.HealthHandler)

	return r
}

func (app *Application) Run() error {
	// 1. Start Worker processes
	if app.Config.Task.WorkerCount > 0 {
		app.Logger.Info(fmt.Sprintf("Starting %d Worker processes", app.Config.Task.WorkerCount))
		app.Workers = worker.StartWorkers(
			app.Config.Task.WorkerCount,
			app.TaskService,
			app.Redis,
			app.Config.Task.QueueName,
			app.Logger,
		)
	}

	// 2. Set up routes
	r := app.setupRouter()

	// 3. Create HTTP Server
	addr := fmt.Sprintf(":%d", app.Config.Server.Port)
	app.HTTPServer = &http.Server{
		Addr:    addr,
		Handler: r,
	}

	go app.gracefulShutdown()

	app.Logger.Info(fmt.Sprintf("Server is starting on %s", addr))
	if err := app.HTTPServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("failed to start server: %w", err)
	}

	return nil
}

func (app *Application) gracefulShutdown() {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	app.Logger.Info("Server is shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := app.HTTPServer.Shutdown(ctx); err != nil {
		app.Logger.Error("Server forced to shutdown", zap.Error(err))
	}

	// Stop all Workers
	for _, w := range app.Workers {
		w.Stop()
	}

	// Stop all Discord listeners
	app.listenersMu.Lock()
	for accountID, listener := range app.Listeners {
		if err := listener.Stop(); err != nil {
			app.Logger.Error("Failed to stop listener", zap.Uint("id", accountID), zap.Error(err))
		}
	}
	app.listenersMu.Unlock()

	// Close database connection
	if app.DB != nil {
		if sqlDB, err := app.DB.DB(); err == nil {
			sqlDB.Close()
		}
	}

	// Close Redis connection
	if app.Redis != nil {
		app.Redis.Close()
	}

	app.Logger.Sync()

	app.Logger.Info("Server has shutdown")
}
