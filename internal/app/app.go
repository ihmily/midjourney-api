package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
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
	app.AccountHandler = handler.NewAccountHandler(app.AccountService)
	app.HealthHandler = handler.NewHealthHandler()

	app.loadAccounts()

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

func (app *Application) loadAccounts() {
	ctx := context.Background()
	app.Logger.Info("Loading accounts from configuration file...")

	for _, accountConfig := range app.Config.Discord.Accounts {
		existingAccount, err := app.AccountRepo.GetByGuildAndChannel(
			ctx,
			accountConfig.GuildID,
			accountConfig.ChannelID,
		)

		if err == nil && existingAccount != nil {
			existingAccount.UserToken = accountConfig.UserToken
			existingAccount.BotToken = accountConfig.BotToken
			if err := app.AccountRepo.Update(ctx, existingAccount); err != nil {
				app.Logger.Warn("Failed to update account",
					zap.String("guild_id", accountConfig.GuildID),
					zap.Error(err))
			} else {
				app.Logger.Info("Account updated",
					zap.Uint("id", existingAccount.ID),
					zap.String("guild_id", accountConfig.GuildID))
			}
			continue
		}

		// Create new account
		account := &model.Account{
			GuildID:         accountConfig.GuildID,
			ChannelID:       accountConfig.ChannelID,
			UserToken:       accountConfig.UserToken,
			BotToken:        accountConfig.BotToken,
			ConcurrentLimit: constants.DefaultConcurrentLimit,
			Status:          model.AccountStatusActive,
			Health:          model.AccountHealthUnknown,
			CurrentJobs:     0,
		}

		if err := app.AccountRepo.Create(ctx, account); err != nil {
			app.Logger.Warn("Failed to create account",
				zap.String("guild_id", accountConfig.GuildID),
				zap.Error(err))
			continue
		}

		app.Logger.Info("Account created",
			zap.Uint("id", account.ID),
			zap.String("guild_id", accountConfig.GuildID))
	}

	totalAccounts := len(app.Config.Discord.Accounts)
	app.Logger.Info("Total accounts configured", zap.Int("count", totalAccounts))
}

func (app *Application) initListeners() {
	ctx := context.Background()

	accounts, err := app.AccountRepo.List(ctx)
	if err != nil {
		app.Logger.Error("Failed to get account list", zap.Error(err))
		return
	}

	app.Listeners = make(map[uint]*discord.Listener)

	type accountInfo struct {
		Name    string `json:"name"`
		GuildID string `json:"guild_id"`
	}
	type botInfo struct {
		Username string `json:"username"`
		UserID   string `json:"user_id"`
	}

	listenedList := make([]accountInfo, 0)
	unlistenedList := make([]accountInfo, 0)

	getAccountName := func(guildID, channelID string) string {
		for _, cfgAcc := range app.Config.Discord.Accounts {
			if cfgAcc.GuildID == guildID && cfgAcc.ChannelID == channelID {
				return cfgAcc.Name
			}
		}
		return ""
	}

	for _, acc := range accounts {
		name := getAccountName(acc.GuildID, acc.ChannelID)
		info := accountInfo{Name: name, GuildID: acc.GuildID}

		if acc.BotToken == "" {
			app.Logger.Warn("Account missing BotToken, skipping listener creation",
				zap.Uint("id", acc.ID),
				zap.String("guild_id", acc.GuildID))
			unlistenedList = append(unlistenedList, info)
			continue
		}

		listener := discord.NewListener(acc.BotToken, app.Config.Discord.ApplicationID, app.DB, app.Logger, app.OSSUploader)
		if listener == nil {
			app.Logger.Error("Failed to create listener",
				zap.Uint("id", acc.ID),
				zap.String("guild_id", acc.GuildID))
			unlistenedList = append(unlistenedList, info)
			app.AccountService.UpdateAccountHealth(ctx, acc.ID, model.AccountHealthUnhealthy, "Failed to create listener")
			continue
		}

		if err := listener.Start(); err != nil {
			app.Logger.Error("Failed to start listener",
				zap.Uint("id", acc.ID),
				zap.String("guild_id", acc.GuildID),
				zap.Error(err))
			unlistenedList = append(unlistenedList, info)
			app.AccountService.UpdateAccountHealth(ctx, acc.ID, model.AccountHealthUnhealthy, "Failed to start listener: "+err.Error())
			continue
		}

		app.Listeners[acc.ID] = listener
		listenedList = append(listenedList, info)
	}

	if len(app.Listeners) > 0 {
		time.Sleep(constants.ListenerStartupWait)
	}

	// Check listener bot info and update account health status
	startedBots := make([]botInfo, 0)
	for accountID, listener := range app.Listeners {
		username, userID := listener.GetBotInfo()
		if username != "" && userID != "" {
			if err := app.AccountService.UpdateAccountHealth(ctx, accountID, model.AccountHealthHealthy, ""); err != nil {
				app.Logger.Error("Failed to update account health",
					zap.Uint("id", accountID),
					zap.Error(err))
			}
			startedBots = append(startedBots, botInfo{Username: username, UserID: userID})
		} else {
			app.Logger.Warn("Listener connected but failed to get bot info, possible invalid token",
				zap.Uint("id", accountID))
			app.AccountService.UpdateAccountHealth(ctx, accountID, model.AccountHealthUnhealthy, "Failed to get bot info")
		}
	}

	listenedJSON, _ := json.Marshal(listenedList)
	unlistenedJSON, _ := json.Marshal(unlistenedList)
	startedJSON, _ := json.Marshal(startedBots)

	app.Logger.Info("Listened accounts: " + string(listenedJSON))
	app.Logger.Info("Unlistened accounts: " + string(unlistenedJSON))
	if len(startedBots) > 0 {
		app.Logger.Info("Started bots: " + string(startedJSON))
	}
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
	for accountID, listener := range app.Listeners {
		if err := listener.Stop(); err != nil {
			app.Logger.Error("Failed to stop listener", zap.Uint("id", accountID), zap.Error(err))
		}
	}

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
