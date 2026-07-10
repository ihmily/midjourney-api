package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trae/midjourney-api/internal/config"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/internal/oss"
	"github.com/trae/midjourney-api/internal/repository"
	"github.com/trae/midjourney-api/internal/service"
	"github.com/trae/midjourney-api/internal/worker"
	"github.com/trae/midjourney-api/pkg/constants"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func TestAccountMatchesListenerConfig(t *testing.T) {
	expected := &model.Account{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserToken: "token-1",
	}

	tests := []struct {
		name    string
		current *model.Account
		want    bool
	}{
		{
			name: "matches active account",
			current: &model.Account{
				GuildID:   "guild-1",
				ChannelID: "channel-1",
				UserToken: "token-1",
			},
			want: true,
		},
		{
			name: "disabled account",
			current: &model.Account{
				GuildID:    "guild-1",
				ChannelID:  "channel-1",
				UserToken:  "token-1",
				IsDisabled: true,
			},
			want: false,
		},
		{
			name: "changed token",
			current: &model.Account{
				GuildID:   "guild-1",
				ChannelID: "channel-1",
				UserToken: "token-2",
			},
			want: false,
		},
		{
			name: "changed channel",
			current: &model.Account{
				GuildID:   "guild-1",
				ChannelID: "channel-2",
				UserToken: "token-1",
			},
			want: false,
		},
		{
			name:    "missing account",
			current: nil,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := accountMatchesListenerConfig(expected, tt.current); got != tt.want {
				t.Fatalf("accountMatchesListenerConfig = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTaskTimeoutSweepInterval(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{
			name:    "non-positive timeout",
			timeout: 0,
			want:    time.Minute,
		},
		{
			name:    "small timeout",
			timeout: time.Second,
			want:    time.Second,
		},
		{
			name:    "normal timeout",
			timeout: 40 * time.Second,
			want:    20 * time.Second,
		},
		{
			name:    "large timeout capped",
			timeout: 10 * time.Minute,
			want:    time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := taskTimeoutSweepInterval(tt.timeout); got != tt.want {
				t.Fatalf("taskTimeoutSweepInterval = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestPostgresDSNEscapesKeywordValues(t *testing.T) {
	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     5432,
		User:     "mj user",
		Password: `pa ss'\word`,
		DBName:   "mid journey",
		SSLMode:  "disable",
	}

	dsn := postgresDSN(cfg)

	for _, expected := range []string{
		"host=localhost",
		"port=5432",
		"user='mj user'",
		`password='pa ss\'\\word'`,
		"dbname='mid journey'",
		"sslmode=disable",
	} {
		if !strings.Contains(dsn, expected) {
			t.Fatalf("dsn = %q, want substring %q", dsn, expected)
		}
	}
}

func TestDatabaseInitErrorRedactsMessageAndPreservesCause(t *testing.T) {
	cause := errors.New(`connect failed password=secret-db-password callback=https://user:pass@example.com/hook?token=secret#frag`)

	err := databaseInitError(cause)

	if err == nil {
		t.Fatal("databaseInitError returned nil")
	}
	if !errors.Is(err, cause) {
		t.Fatal("databaseInitError did not preserve the original cause")
	}
	message := err.Error()
	for _, forbidden := range []string{"secret-db-password", "token=secret", "user:pass", "#frag"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("database init error exposed %q: %s", forbidden, message)
		}
	}
	if !strings.Contains(message, "password=<redacted>") || !strings.Contains(message, "https://example.com/hook") {
		t.Fatalf("database init error did not keep useful redacted context: %s", message)
	}
}

func TestStopWorkersIsIdempotent(t *testing.T) {
	app := &Application{
		Workers: []*worker.Worker{
			nil,
			worker.NewWorker(nil, nil, "queue", time.Second, zap.NewNop()),
		},
	}

	app.stopWorkers()
	app.stopWorkers()
}

func TestStopAccountListenerAllowsNilListenerEntry(t *testing.T) {
	app := &Application{
		Listeners: map[uint]accountListener{7: nil},
	}

	if err := app.StopAccountListener(7); err != nil {
		t.Fatalf("StopAccountListener returned error: %v", err)
	}
	if _, ok := app.Listeners[7]; ok {
		t.Fatalf("listener entry was not removed")
	}
}

func TestStopAccountListenerInvalidatesStartBeforeRegistration(t *testing.T) {
	account := &model.Account{
		ID:        7,
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserToken: "token-1",
	}
	accountService := &fakeListenerAccountService{account: account}
	listener := newFakeAccountListener()
	releaseStart := make(chan struct{})
	listener.startBlock = releaseStart

	app := &Application{
		AccountService:      accountService,
		Logger:              zap.NewNop(),
		Listeners:           make(map[uint]accountListener),
		listenerGenerations: make(map[uint]uint64),
		newAccountListener:  func(string, string, *gorm.DB, *zap.Logger, oss.Uploader) accountListener { return listener },
		listenerStartupWait: 10 * time.Millisecond,
	}

	done := make(chan error, 1)
	go func() {
		done <- app.StartAccountListener(account)
	}()

	select {
	case <-listener.started:
	case <-time.After(time.Second):
		t.Fatal("listener start was not reached")
	}

	start := time.Now()
	if err := app.StopAccountListener(account.ID); err != nil {
		t.Fatalf("StopAccountListener returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("StopAccountListener took %s, want it to return without waiting for listener start", elapsed)
	}
	if got := listener.stopCount(); got != 0 {
		t.Fatalf("listener stop count before start release = %d, want 0", got)
	}

	select {
	case <-releaseStart:
	default:
		close(releaseStart)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StartAccountListener returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StartAccountListener did not complete after listener start was released")
	}
	if _, ok := app.Listeners[7]; ok {
		t.Fatalf("listener entry was not removed")
	}
	if got := listener.stopCount(); got != 1 {
		t.Fatalf("listener stop count after stale start = %d, want 1", got)
	}
	if got := accountService.healthUpdateCount(); got != 0 {
		t.Fatalf("health update count = %d, want 0 for stale listener", got)
	}
}

func TestStopAccountListenerInvalidatesStartDuringStartupWait(t *testing.T) {
	account := &model.Account{
		ID:        7,
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserToken: "token-1",
	}
	accountService := &fakeListenerAccountService{account: account}
	listener := newFakeAccountListener()

	app := &Application{
		AccountService:      accountService,
		Logger:              zap.NewNop(),
		Listeners:           make(map[uint]accountListener),
		listenerGenerations: make(map[uint]uint64),
		newAccountListener:  func(string, string, *gorm.DB, *zap.Logger, oss.Uploader) accountListener { return listener },
		listenerStartupWait: 200 * time.Millisecond,
	}

	done := make(chan error, 1)
	go func() {
		done <- app.StartAccountListener(account)
	}()

	waitForListenerEntry(t, app, account.ID)

	start := time.Now()
	if err := app.StopAccountListener(account.ID); err != nil {
		t.Fatalf("StopAccountListener returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("StopAccountListener took %s, want it to return during startup wait", elapsed)
	}

	select {
	case <-listener.stopped:
	case <-time.After(time.Second):
		t.Fatal("listener was not stopped")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StartAccountListener returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StartAccountListener did not complete")
	}
	if _, ok := app.Listeners[account.ID]; ok {
		t.Fatalf("listener entry was not removed")
	}
	if got := listener.stopCount(); got != 1 {
		t.Fatalf("listener stop count = %d, want 1", got)
	}
	if got := accountService.healthUpdateCount(); got != 0 {
		t.Fatalf("health update count = %d, want 0 for stopped listener", got)
	}
}

func TestStartAccountListenerSkipsDeletedAccountBeforeStarting(t *testing.T) {
	account := &model.Account{
		ID:        7,
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserToken: "token-1",
	}
	accountService := &fakeListenerAccountService{}
	listener := newFakeAccountListener()
	factoryCalls := 0

	app := &Application{
		AccountService:      accountService,
		Logger:              zap.NewNop(),
		Listeners:           make(map[uint]accountListener),
		listenerGenerations: make(map[uint]uint64),
		newAccountListener: func(string, string, *gorm.DB, *zap.Logger, oss.Uploader) accountListener {
			factoryCalls++
			return listener
		},
	}

	if err := app.StartAccountListener(account); err != nil {
		t.Fatalf("StartAccountListener returned error: %v", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("listener factory calls = %d, want 0 for deleted account", factoryCalls)
	}
	select {
	case <-listener.started:
		t.Fatal("listener was started for a deleted account")
	default:
	}
	if len(app.Listeners) != 0 {
		t.Fatalf("listeners len = %d, want 0", len(app.Listeners))
	}
	if got := accountService.healthUpdateCount(); got != 0 {
		t.Fatalf("health update count = %d, want 0 for stale start", got)
	}
}

func TestStartAccountListenerRestoresExistingListenerWhenAccountChangesAfterPrepare(t *testing.T) {
	account := &model.Account{
		ID:        7,
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserToken: "token-1",
	}
	accountService := &sequenceListenerAccountService{
		accounts: []*model.Account{
			{
				ID:        7,
				GuildID:   "guild-1",
				ChannelID: "channel-1",
				UserToken: "token-1",
			},
			{
				ID:        7,
				GuildID:   "guild-1",
				ChannelID: "channel-1",
				UserToken: "token-2",
			},
		},
	}
	existingListener := newFakeAccountListener()
	newListener := newFakeAccountListener()
	factoryCalls := 0

	app := &Application{
		AccountService: accountService,
		Logger:         zap.NewNop(),
		Listeners: map[uint]accountListener{
			account.ID: existingListener,
		},
		listenerGenerations: map[uint]uint64{
			account.ID: 4,
		},
		newAccountListener: func(string, string, *gorm.DB, *zap.Logger, oss.Uploader) accountListener {
			factoryCalls++
			return newListener
		},
	}

	if err := app.StartAccountListener(account); err != nil {
		t.Fatalf("StartAccountListener returned error: %v", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("listener factory calls = %d, want 0 for stale start", factoryCalls)
	}
	if got := existingListener.stopCount(); got != 0 {
		t.Fatalf("existing listener stop count = %d, want 0", got)
	}
	if app.Listeners[account.ID] != existingListener {
		t.Fatal("existing listener was not restored")
	}
	if generation := app.listenerGenerations[account.ID]; generation != 4 {
		t.Fatalf("listener generation = %d, want restored generation 4", generation)
	}
	select {
	case <-newListener.started:
		t.Fatal("new listener was started for a stale account snapshot")
	default:
	}
}

func TestStartAccountListenerDoesNotRestoreGenerationWithoutExistingListener(t *testing.T) {
	account := &model.Account{
		ID:        7,
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserToken: "token-1",
	}
	accountService := &sequenceListenerAccountService{
		accounts: []*model.Account{
			{
				ID:        7,
				GuildID:   "guild-1",
				ChannelID: "channel-1",
				UserToken: "token-1",
			},
			{
				ID:        7,
				GuildID:   "guild-1",
				ChannelID: "channel-1",
				UserToken: "token-2",
			},
		},
	}
	newListener := newFakeAccountListener()
	factoryCalls := 0

	app := &Application{
		AccountService: accountService,
		Logger:         zap.NewNop(),
		Listeners:      make(map[uint]accountListener),
		listenerGenerations: map[uint]uint64{
			account.ID: 4,
		},
		newAccountListener: func(string, string, *gorm.DB, *zap.Logger, oss.Uploader) accountListener {
			factoryCalls++
			return newListener
		},
	}

	if err := app.StartAccountListener(account); err != nil {
		t.Fatalf("StartAccountListener returned error: %v", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("listener factory calls = %d, want 0 for stale start", factoryCalls)
	}
	if _, ok := app.Listeners[account.ID]; ok {
		t.Fatal("listener entry should stay empty")
	}
	if generation := app.listenerGenerations[account.ID]; generation != 5 {
		t.Fatalf("listener generation = %d, want 5 to keep older starts invalidated", generation)
	}
	if app.accountListenerGenerationCurrent(account.ID, 4) {
		t.Fatal("older in-flight generation was restored")
	}
	select {
	case <-newListener.started:
		t.Fatal("new listener was started for a stale account snapshot")
	default:
	}
}

func TestStopAllAccountListenersAllowsNilEntries(t *testing.T) {
	app := &Application{
		Listeners: map[uint]accountListener{
			7: nil,
		},
	}

	app.stopAllAccountListeners()
	app.stopAllAccountListeners()

	if len(app.Listeners) != 0 {
		t.Fatalf("listeners len = %d, want 0", len(app.Listeners))
	}
}

func TestStopAllAccountListenersInvalidatesInFlightStarts(t *testing.T) {
	account := &model.Account{
		ID:        7,
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserToken: "token-1",
	}
	accountService := &fakeListenerAccountService{account: account}
	listener := newFakeAccountListener()
	releaseStart := make(chan struct{})
	listener.startBlock = releaseStart

	app := &Application{
		AccountService:      accountService,
		Logger:              zap.NewNop(),
		Listeners:           make(map[uint]accountListener),
		listenerGenerations: make(map[uint]uint64),
		newAccountListener:  func(string, string, *gorm.DB, *zap.Logger, oss.Uploader) accountListener { return listener },
		listenerStartupWait: 10 * time.Millisecond,
	}

	done := make(chan error, 1)
	go func() {
		done <- app.StartAccountListener(account)
	}()

	select {
	case <-listener.started:
	case <-time.After(time.Second):
		t.Fatal("listener start was not reached")
	}

	app.stopAllAccountListeners()
	close(releaseStart)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StartAccountListener returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StartAccountListener did not complete")
	}
	if len(app.Listeners) != 0 {
		t.Fatalf("listeners len = %d, want 0", len(app.Listeners))
	}
	if got := listener.stopCount(); got != 1 {
		t.Fatalf("listener stop count = %d, want 1 because shutdown made the in-flight listener stale", got)
	}
	if got := accountService.healthUpdateCount(); got != 0 {
		t.Fatalf("health update count = %d, want 0 for shutdown listener", got)
	}
}

func TestStartAccountListenersAsyncStartsDatabaseAccounts(t *testing.T) {
	account := model.Account{
		ID:              7,
		GuildID:         "guild-1",
		ChannelID:       "channel-1",
		UserToken:       "token-1",
		ConcurrentLimit: constants.DefaultConcurrentLimit,
	}
	accountService := &fakeListenerAccountService{account: &account}
	listener := newFakeAccountListener()
	app := &Application{
		DB:                  &gorm.DB{},
		Logger:              zap.NewNop(),
		AccountRepo:         fakeAccountListRepo{accounts: []model.Account{account}},
		AccountService:      accountService,
		Listeners:           make(map[uint]accountListener),
		listenerGenerations: make(map[uint]uint64),
		newAccountListener:  func(string, string, *gorm.DB, *zap.Logger, oss.Uploader) accountListener { return listener },
	}

	app.startAccountListenersAsync()
	waitForListenerEntry(t, app, account.ID)

	select {
	case <-listener.started:
	default:
		t.Fatal("listener was not started")
	}
	if got := waitForHealthUpdateCount(t, accountService, 1); got != 1 {
		t.Fatalf("health update count = %d, want 1", got)
	}

	app.stopAllAccountListeners()
	if got := listener.stopCount(); got != 1 {
		t.Fatalf("listener stop count = %d, want 1", got)
	}
}

func TestShutdownHelpersAllowNilApplicationState(t *testing.T) {
	var nilApp *Application

	nilApp.shutdownHTTPServer(context.Background())
	nilApp.stopAllAccountListeners()
	nilApp.closeResources()
	nilApp.stopTaskTimeoutSweeper()
	nilApp.shutdownRuntime(context.Background())

	app := &Application{}
	app.shutdownHTTPServer(context.Background())
	app.stopAllAccountListeners()
	app.closeResources()
	app.stopTaskTimeoutSweeper()
	app.shutdownRuntime(context.Background())

	if app.Listeners == nil {
		t.Fatalf("listeners map should be initialized after stopAllAccountListeners")
	}
	if app.logger() == nil {
		t.Fatalf("nil logger should return a no-op logger")
	}
}

func TestGracefulShutdownStopsWithoutSignal(t *testing.T) {
	app := &Application{}
	stop := make(chan struct{})
	started := make(chan struct{})
	done := make(chan struct{})

	go app.gracefulShutdown(stop, started, done)
	close(stop)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("gracefulShutdown did not stop")
	}

	select {
	case <-started:
		t.Fatal("shutdown should not be marked as started when stop channel is closed")
	default:
	}
}

func TestSweepTaskTimeoutsAllowsMissingTaskService(t *testing.T) {
	var nilApp *Application
	nilApp.sweepTaskTimeouts(context.Background(), time.Second)

	app := &Application{}
	app.sweepTaskTimeouts(context.Background(), time.Second)
}

func TestSweepTaskTimeoutsUsesNoopLoggerWhenSweepFails(t *testing.T) {
	app := &Application{
		TaskService: failingSweepTaskService{err: errors.New("sweep failed")},
	}

	app.sweepTaskTimeouts(context.Background(), time.Second)
}

func TestStartTaskTimeoutSweeperCancelsExistingSweeper(t *testing.T) {
	cancelled := make(chan struct{})
	app := &Application{
		timeoutSweepCancel: func() {
			close(cancelled)
		},
	}

	app.startTaskTimeoutSweeper(time.Hour)
	defer app.stopTaskTimeoutSweeper()

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("existing timeout sweeper was not cancelled")
	}
}

func TestStopTaskTimeoutSweeperIsIdempotentAndClearsCancel(t *testing.T) {
	cancelCalls := 0
	app := &Application{
		timeoutSweepCancel: func() {
			cancelCalls++
		},
	}

	app.stopTaskTimeoutSweeper()
	app.stopTaskTimeoutSweeper()

	if cancelCalls != 1 {
		t.Fatalf("cancelCalls = %d, want 1", cancelCalls)
	}
	if app.timeoutSweepCancel != nil {
		t.Fatal("timeoutSweepCancel was not cleared")
	}
}

func TestCORSConfigDoesNotAllowWildcardCredentials(t *testing.T) {
	cfg := corsConfig()

	if cfg.AllowCredentials {
		t.Fatalf("AllowCredentials = true, want false when AllowOrigins contains wildcard")
	}
	if len(cfg.AllowOrigins) != 1 || cfg.AllowOrigins[0] != "*" {
		t.Fatalf("AllowOrigins = %#v, want wildcard origin", cfg.AllowOrigins)
	}
}

func TestNewHTTPServerSetsDefensiveTimeouts(t *testing.T) {
	handler := http.NewServeMux()
	server := newHTTPServer(":8080", handler)

	if server.Addr != ":8080" {
		t.Fatalf("Addr = %q, want :8080", server.Addr)
	}
	if server.Handler != handler {
		t.Fatal("HTTP server handler was not preserved")
	}
	if server.ReadTimeout != constants.ServerReadTimeout {
		t.Fatalf("ReadTimeout = %s, want %s", server.ReadTimeout, constants.ServerReadTimeout)
	}
	if server.ReadHeaderTimeout != constants.ServerReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %s, want %s", server.ReadHeaderTimeout, constants.ServerReadHeaderTimeout)
	}
	if server.WriteTimeout != constants.ServerWriteTimeout {
		t.Fatalf("WriteTimeout = %s, want %s", server.WriteTimeout, constants.ServerWriteTimeout)
	}
	if server.IdleTimeout != constants.ServerIdleTimeout {
		t.Fatalf("IdleTimeout = %s, want %s", server.IdleTimeout, constants.ServerIdleTimeout)
	}
	if server.MaxHeaderBytes != constants.ServerMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d, want %d", server.MaxHeaderBytes, constants.ServerMaxHeaderBytes)
	}
}

type failingSweepTaskService struct {
	service.TaskService
	err error
}

func (s failingSweepTaskService) SweepTimedOutTasks(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	return 0, s.err
}

type fakeAccountListener struct {
	startBlock <-chan struct{}
	started    chan struct{}
	stopped    chan struct{}

	mu       sync.Mutex
	stopOnce sync.Once
	stops    int
}

func newFakeAccountListener() *fakeAccountListener {
	return &fakeAccountListener{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

func (l *fakeAccountListener) Start() error {
	close(l.started)
	if l.startBlock != nil {
		<-l.startBlock
	}
	return nil
}

func (l *fakeAccountListener) Stop() error {
	l.mu.Lock()
	l.stops++
	l.mu.Unlock()
	l.stopOnce.Do(func() {
		close(l.stopped)
	})
	return nil
}

func (l *fakeAccountListener) GetBotInfo() (string, string) {
	return "bot", "bot-id"
}

func (l *fakeAccountListener) stopCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stops
}

type fakeListenerAccountService struct {
	service.AccountService

	mu            sync.Mutex
	account       *model.Account
	healthUpdates int
}

func (s *fakeListenerAccountService) GetAccountByID(ctx context.Context, id uint) (*model.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.account == nil || s.account.ID != id {
		return nil, apperrors.NewAccountNotFound(id)
	}
	account := *s.account
	return &account, nil
}

func (s *fakeListenerAccountService) SetAccountHealthy(ctx context.Context, id uint, isHealthy bool, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.healthUpdates++
	if s.account != nil && s.account.ID == id {
		s.account.IsHealthy = isHealthy
		s.account.LastError = lastError
	}
	return nil
}

func (s *fakeListenerAccountService) healthUpdateCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.healthUpdates
}

type sequenceListenerAccountService struct {
	service.AccountService

	mu       sync.Mutex
	accounts []*model.Account
	calls    int
}

func (s *sequenceListenerAccountService) GetAccountByID(ctx context.Context, id uint) (*model.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.accounts) == 0 {
		return nil, apperrors.NewAccountNotFound(id)
	}
	index := s.calls
	if index >= len(s.accounts) {
		index = len(s.accounts) - 1
	}
	s.calls++

	account := s.accounts[index]
	if account == nil || account.ID != id {
		return nil, apperrors.NewAccountNotFound(id)
	}
	copy := *account
	return &copy, nil
}

type fakeAccountListRepo struct {
	repository.AccountRepository

	accounts []model.Account
	err      error
}

func (r fakeAccountListRepo) List(ctx context.Context) ([]model.Account, error) {
	if r.err != nil {
		return nil, r.err
	}
	accounts := make([]model.Account, len(r.accounts))
	copy(accounts, r.accounts)
	return accounts, nil
}

func waitForListenerEntry(t *testing.T, app *Application, accountID uint) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		app.listenersMu.RLock()
		_, ok := app.Listeners[accountID]
		app.listenersMu.RUnlock()
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("listener entry was not registered")
}

func waitForHealthUpdateCount(t *testing.T, service *fakeListenerAccountService, min int) int {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		count := service.healthUpdateCount()
		if count >= min {
			return count
		}
		time.Sleep(5 * time.Millisecond)
	}
	return service.healthUpdateCount()
}
