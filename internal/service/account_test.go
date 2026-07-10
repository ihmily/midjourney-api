package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/pkg/constants"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"go.uber.org/zap"
)

func TestCreateAccountUsesDefaultConcurrentLimit(t *testing.T) {
	repo := &fakeAccountRepo{}
	svc := &accountService{
		accountRepo: repo,
		logger:      zap.NewNop(),
	}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountRequest{
		GuildID:   " guild-1 ",
		ChannelID: " channel-1 ",
		UserToken: " token ",
	})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}

	if account.ConcurrentLimit != constants.DefaultConcurrentLimit {
		t.Fatalf("concurrent_limit = %d, want %d", account.ConcurrentLimit, constants.DefaultConcurrentLimit)
	}
	if account.CurrentJobs != 0 {
		t.Fatalf("current_jobs = %d, want 0", account.CurrentJobs)
	}
	if repo.created == nil || repo.created.UserToken != "token" {
		t.Fatalf("created account was not persisted through repository")
	}
	if repo.created.GuildID != "guild-1" || repo.created.ChannelID != "channel-1" {
		t.Fatalf("account was not trimmed: %#v", repo.created)
	}
}

func TestNewAccountServiceDefaultsLogger(t *testing.T) {
	svc, ok := NewAccountService(&fakeAccountRepo{}, nil).(*accountService)
	if !ok {
		t.Fatalf("NewAccountService returned %T, want *accountService", svc)
	}
	if svc.logger == nil {
		t.Fatal("logger was nil")
	}
}

func TestAccountServiceRedactsLastErrorOnRead(t *testing.T) {
	repo := &fakeAccountRepo{
		account: &model.Account{
			ID:        11,
			LastError: `discord API error: user_token="secret-token" callback=https://user:pass@example.com/hook?token=secret#frag`,
		},
		accounts: []model.Account{
			{
				ID:        12,
				LastError: `api_key=secret-key callback=https://example.com/hook?token=secret`,
			},
		},
	}
	svc := &accountService{
		accountRepo: repo,
		logger:      zap.NewNop(),
	}

	account, err := svc.GetAccountByID(context.Background(), 11)
	if err != nil {
		t.Fatalf("GetAccountByID returned error: %v", err)
	}
	assertRedactedAccountLastError(t, account.LastError)

	accounts, err := svc.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("ListAccounts returned error: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("len(accounts) = %d, want 1", len(accounts))
	}
	assertRedactedAccountLastError(t, accounts[0].LastError)
}

func TestUpdateAccountRedactsLastErrorOnReturn(t *testing.T) {
	repo := &fakeAccountRepo{
		account: &model.Account{
			ID:              11,
			GuildID:         "guild-1",
			ChannelID:       "channel-1",
			UserToken:       "token",
			ConcurrentLimit: 10,
			LastError:       `discord API error: api_key=secret-key callback=https://user:pass@example.com/hook?token=secret#frag`,
		},
	}
	svc := &accountService{
		accountRepo: repo,
		logger:      zap.NewNop(),
	}
	limit := 20

	account, err := svc.UpdateAccount(context.Background(), 11, &UpdateAccountRequest{
		ConcurrentLimit: &limit,
	})
	if err != nil {
		t.Fatalf("UpdateAccount returned error: %v", err)
	}
	if account.ConcurrentLimit != limit {
		t.Fatalf("concurrent_limit = %d, want %d", account.ConcurrentLimit, limit)
	}
	assertRedactedAccountLastError(t, account.LastError)
	assertRedactedAccountLastError(t, repo.updated.LastError)
}

func assertRedactedAccountLastError(t *testing.T, lastError string) {
	t.Helper()

	for _, forbidden := range []string{"secret-token", "secret-key", "token=secret", "user:pass", "#frag"} {
		if strings.Contains(lastError, forbidden) {
			t.Fatalf("last_error exposed %q: %s", forbidden, lastError)
		}
	}
	if !strings.Contains(lastError, "<redacted>") {
		t.Fatalf("last_error missing redaction marker: %s", lastError)
	}
}

func TestAccountServiceRejectsMissingRepository(t *testing.T) {
	tests := []struct {
		name string
		svc  *accountService
		run  func(context.Context, *accountService) error
	}{
		{
			name: "nil receiver create",
			svc:  nil,
			run: func(ctx context.Context, svc *accountService) error {
				_, err := svc.CreateAccount(ctx, &CreateAccountRequest{
					GuildID:   "guild-1",
					ChannelID: "channel-1",
					UserToken: "token",
				})
				return err
			},
		},
		{
			name: "create",
			svc:  &accountService{},
			run: func(ctx context.Context, svc *accountService) error {
				_, err := svc.CreateAccount(ctx, &CreateAccountRequest{
					GuildID:   "guild-1",
					ChannelID: "channel-1",
					UserToken: "token",
				})
				return err
			},
		},
		{
			name: "get",
			svc:  &accountService{},
			run: func(ctx context.Context, svc *accountService) error {
				_, err := svc.GetAccountByID(ctx, 11)
				return err
			},
		},
		{
			name: "list",
			svc:  &accountService{},
			run: func(ctx context.Context, svc *accountService) error {
				_, err := svc.ListAccounts(ctx)
				return err
			},
		},
		{
			name: "update",
			svc:  &accountService{},
			run: func(ctx context.Context, svc *accountService) error {
				limit := 20
				_, err := svc.UpdateAccount(ctx, 11, &UpdateAccountRequest{
					ConcurrentLimit: &limit,
				})
				return err
			},
		},
		{
			name: "delete",
			svc:  &accountService{},
			run: func(ctx context.Context, svc *accountService) error {
				return svc.DeleteAccount(ctx, 11)
			},
		},
		{
			name: "acquire available",
			svc:  &accountService{},
			run: func(ctx context.Context, svc *accountService) error {
				_, err := svc.AcquireAvailableAccount(ctx)
				return err
			},
		},
		{
			name: "acquire by id",
			svc:  &accountService{},
			run: func(ctx context.Context, svc *accountService) error {
				_, err := svc.AcquireAccount(ctx, 11)
				return err
			},
		},
		{
			name: "set healthy",
			svc:  &accountService{},
			run: func(ctx context.Context, svc *accountService) error {
				return svc.SetAccountHealthy(ctx, 11, true, "")
			},
		},
		{
			name: "decrement jobs",
			svc:  &accountService{},
			run: func(ctx context.Context, svc *accountService) error {
				return svc.DecrementJobs(ctx, 11)
			},
		},
		{
			name: "record task result",
			svc:  &accountService{},
			run: func(ctx context.Context, svc *accountService) error {
				return svc.RecordTaskResult(ctx, 11, true, "")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run(context.Background(), tt.svc)

			assertAppErrorCode(t, err, apperrors.ErrCodeInternal)
		})
	}
}

func TestCreateAccountRejectsBlankInput(t *testing.T) {
	repo := &fakeAccountRepo{}
	svc := &accountService{
		accountRepo: repo,
		logger:      zap.NewNop(),
	}

	_, err := svc.CreateAccount(context.Background(), &CreateAccountRequest{
		GuildID:   "   ",
		ChannelID: "channel-1",
		UserToken: "token",
	})

	assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
	if repo.created != nil {
		t.Fatalf("blank account should not be created")
	}
}

func TestCreateAccountRejectsOverlongInput(t *testing.T) {
	tests := []struct {
		name string
		req  CreateAccountRequest
	}{
		{
			name: "guild id",
			req: CreateAccountRequest{
				GuildID:   strings.Repeat("g", constants.MaxAccountGuildIDLength+1),
				ChannelID: "channel-1",
				UserToken: "token",
			},
		},
		{
			name: "channel id",
			req: CreateAccountRequest{
				GuildID:   "guild-1",
				ChannelID: strings.Repeat("c", constants.MaxAccountChannelIDLength+1),
				UserToken: "token",
			},
		},
		{
			name: "user token",
			req: CreateAccountRequest{
				GuildID:   "guild-1",
				ChannelID: "channel-1",
				UserToken: strings.Repeat("t", constants.MaxAccountUserTokenLength+1),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeAccountRepo{}
			svc := &accountService{
				accountRepo: repo,
				logger:      zap.NewNop(),
			}

			_, err := svc.CreateAccount(context.Background(), &tt.req)

			assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
			if repo.created != nil {
				t.Fatalf("overlong account should not be created")
			}
			if repo.getByGuildAndChannelCalls != 0 {
				t.Fatalf("GetByGuildAndChannel calls = %d, want 0", repo.getByGuildAndChannelCalls)
			}
		})
	}
}

func TestCreateAccountRejectsNilRequest(t *testing.T) {
	repo := &fakeAccountRepo{}
	svc := &accountService{
		accountRepo: repo,
		logger:      zap.NewNop(),
	}

	_, err := svc.CreateAccount(context.Background(), nil)

	assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
	if repo.created != nil {
		t.Fatalf("nil request should not be created")
	}
	if repo.getByGuildAndChannelCalls != 0 {
		t.Fatalf("GetByGuildAndChannel calls = %d, want 0", repo.getByGuildAndChannelCalls)
	}
}

func TestCreateAccountRejectsDuplicateGuildChannel(t *testing.T) {
	repo := &fakeAccountRepo{
		existingByGuildChannel: &model.Account{
			ID:        99,
			GuildID:   "guild-1",
			ChannelID: "channel-1",
		},
	}
	svc := &accountService{
		accountRepo: repo,
		logger:      zap.NewNop(),
	}

	_, err := svc.CreateAccount(context.Background(), &CreateAccountRequest{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserToken: "token",
	})

	assertAppErrorCode(t, err, apperrors.ErrCodeAccountAlreadyExists)
	if repo.created != nil {
		t.Fatalf("duplicate account should not be created")
	}
}

func TestUpdateAccountRejectsOverlongInput(t *testing.T) {
	tests := []struct {
		name string
		req  UpdateAccountRequest
	}{
		{
			name: "guild id",
			req: UpdateAccountRequest{
				GuildID: stringPtr(strings.Repeat("g", constants.MaxAccountGuildIDLength+1)),
			},
		},
		{
			name: "channel id",
			req: UpdateAccountRequest{
				ChannelID: stringPtr(strings.Repeat("c", constants.MaxAccountChannelIDLength+1)),
			},
		},
		{
			name: "user token",
			req: UpdateAccountRequest{
				UserToken: stringPtr(strings.Repeat("t", constants.MaxAccountUserTokenLength+1)),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeAccountRepo{
				account: &model.Account{
					ID:              11,
					GuildID:         "guild-1",
					ChannelID:       "channel-1",
					UserToken:       "token",
					ConcurrentLimit: 20,
				},
			}
			svc := &accountService{
				accountRepo: repo,
				logger:      zap.NewNop(),
			}

			_, err := svc.UpdateAccount(context.Background(), 11, &tt.req)

			assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
			if repo.updated != nil {
				t.Fatalf("overlong update should not be persisted")
			}
		})
	}
}

func TestUpdateAccountRejectsNilRequestBeforeLookup(t *testing.T) {
	repo := &fakeAccountRepo{}
	svc := &accountService{
		accountRepo: repo,
		logger:      zap.NewNop(),
	}

	_, err := svc.UpdateAccount(context.Background(), 11, nil)

	assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
	if repo.getByIDCalls != 0 {
		t.Fatalf("GetByID calls = %d, want 0", repo.getByIDCalls)
	}
	if repo.updated != nil {
		t.Fatalf("nil update should not be persisted")
	}
}

func TestUpdateAccountRejectsEmptyRequestBeforeLookup(t *testing.T) {
	repo := &fakeAccountRepo{}
	svc := &accountService{
		accountRepo: repo,
		logger:      zap.NewNop(),
	}

	_, err := svc.UpdateAccount(context.Background(), 11, &UpdateAccountRequest{})

	assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
	if repo.getByIDCalls != 0 {
		t.Fatalf("GetByID calls = %d, want 0", repo.getByIDCalls)
	}
	if repo.updated != nil {
		t.Fatalf("empty update should not be persisted")
	}
}

func TestUpdateAccountLimitChangePreservesRuntimeHealthAndJobs(t *testing.T) {
	repo := &fakeAccountRepo{
		account: &model.Account{
			ID:              11,
			GuildID:         "guild-1",
			ChannelID:       "channel-1",
			UserToken:       "token",
			CurrentJobs:     5,
			ConcurrentLimit: 20,
			ErrorCount:      2,
			LastError:       "temporary failure",
			IsHealthy:       false,
		},
	}
	svc := &accountService{
		accountRepo: repo,
		logger:      zap.NewNop(),
	}

	limit := 25
	account, err := svc.UpdateAccount(context.Background(), 11, &UpdateAccountRequest{
		ConcurrentLimit: &limit,
	})
	if err != nil {
		t.Fatalf("UpdateAccount returned error: %v", err)
	}

	if account.IsHealthy {
		t.Fatalf("is_healthy = true, want unchanged false")
	}
	if account.ErrorCount != 2 {
		t.Fatalf("error_count = %d, want unchanged 2", account.ErrorCount)
	}
	if account.LastError != "temporary failure" {
		t.Fatalf("last_error = %q, want unchanged temporary failure", account.LastError)
	}
	if account.CurrentJobs != 5 {
		t.Fatalf("current_jobs = %d, want 5", account.CurrentJobs)
	}
	if repo.updated == nil || repo.updated.CurrentJobs != 5 {
		t.Fatalf("updated account did not preserve current_jobs")
	}
	if repo.updated.IsHealthy {
		t.Fatalf("updated account health was changed by limit update")
	}
	if repo.resetRuntime {
		t.Fatalf("limit update should not request runtime reset")
	}
}

func TestUpdateAccountConfigChangeResetsHealthUntilListenerVerifies(t *testing.T) {
	repo := &fakeAccountRepo{
		account: &model.Account{
			ID:              11,
			GuildID:         "guild-1",
			ChannelID:       "channel-1",
			UserToken:       "old-token",
			CurrentJobs:     0,
			ConcurrentLimit: 20,
			ErrorCount:      2,
			LastError:       "previous failure",
			IsHealthy:       true,
		},
	}
	svc := &accountService{
		accountRepo: repo,
		logger:      zap.NewNop(),
	}

	account, err := svc.UpdateAccount(context.Background(), 11, &UpdateAccountRequest{
		UserToken: stringPtr(" new-token "),
	})
	if err != nil {
		t.Fatalf("UpdateAccount returned error: %v", err)
	}

	if account.UserToken != "new-token" {
		t.Fatalf("user_token = %q, want trimmed new token", account.UserToken)
	}
	if account.IsHealthy {
		t.Fatalf("is_healthy = true, want false until listener verifies changed config")
	}
	if account.ErrorCount != 0 {
		t.Fatalf("error_count = %d, want 0", account.ErrorCount)
	}
	if account.LastError != "" {
		t.Fatalf("last_error = %q, want empty", account.LastError)
	}
	if account.CurrentJobs != 0 {
		t.Fatalf("current_jobs = %d, want 0", account.CurrentJobs)
	}
	if repo.updated == nil || repo.updated.IsHealthy {
		t.Fatalf("updated account health was not reset")
	}
	if !repo.resetRuntime {
		t.Fatalf("config change should request runtime reset")
	}
}

func TestUpdateAccountDisableOrReactivateResetsHealth(t *testing.T) {
	tests := []struct {
		name        string
		wasDisabled bool
		reqDisabled bool
	}{
		{
			name:        "disable",
			wasDisabled: false,
			reqDisabled: true,
		},
		{
			name:        "reactivate",
			wasDisabled: true,
			reqDisabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeAccountRepo{
				account: &model.Account{
					ID:              11,
					GuildID:         "guild-1",
					ChannelID:       "channel-1",
					UserToken:       "token",
					ConcurrentLimit: 20,
					IsDisabled:      tt.wasDisabled,
					IsHealthy:       true,
					ErrorCount:      1,
					LastError:       "previous failure",
				},
			}
			svc := &accountService{
				accountRepo: repo,
				logger:      zap.NewNop(),
			}

			account, err := svc.UpdateAccount(context.Background(), 11, &UpdateAccountRequest{
				IsDisabled: &tt.reqDisabled,
			})
			if err != nil {
				t.Fatalf("UpdateAccount returned error: %v", err)
			}

			if account.IsDisabled != tt.reqDisabled {
				t.Fatalf("is_disabled = %v, want %v", account.IsDisabled, tt.reqDisabled)
			}
			if account.IsHealthy {
				t.Fatalf("is_healthy = true, want false")
			}
			if account.ErrorCount != 0 || account.LastError != "" {
				t.Fatalf("health metadata = count %d error %q, want reset", account.ErrorCount, account.LastError)
			}
			if !repo.resetRuntime {
				t.Fatalf("%s should request runtime reset", tt.name)
			}
		})
	}
}

func TestUpdateAccountRejectsListenerInterruptingChangesWithActiveJobs(t *testing.T) {
	disable := true
	tests := []struct {
		name string
		req  UpdateAccountRequest
	}{
		{
			name: "guild",
			req:  UpdateAccountRequest{GuildID: stringPtr("guild-2")},
		},
		{
			name: "channel",
			req:  UpdateAccountRequest{ChannelID: stringPtr("channel-2")},
		},
		{
			name: "token",
			req:  UpdateAccountRequest{UserToken: stringPtr("token-2")},
		},
		{
			name: "disable",
			req:  UpdateAccountRequest{IsDisabled: &disable},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeAccountRepo{
				account: &model.Account{
					ID:              11,
					GuildID:         "guild-1",
					ChannelID:       "channel-1",
					UserToken:       "token",
					CurrentJobs:     2,
					ConcurrentLimit: 20,
					IsDisabled:      false,
				},
			}
			svc := &accountService{
				accountRepo: repo,
				logger:      zap.NewNop(),
			}

			_, err := svc.UpdateAccount(context.Background(), 11, &tt.req)

			assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
			if repo.updated != nil {
				t.Fatalf("active account change should not be persisted")
			}
			if repo.getByGuildAndChannelCalls != 0 {
				t.Fatalf("duplicate lookup calls = %d, want 0 when active jobs block first", repo.getByGuildAndChannelCalls)
			}
		})
	}
}

func TestUpdateAccountRejectsInvalidConcurrentLimit(t *testing.T) {
	repo := &fakeAccountRepo{
		account: &model.Account{
			ID:              11,
			GuildID:         "guild-1",
			ChannelID:       "channel-1",
			UserToken:       "token",
			ConcurrentLimit: 20,
		},
	}
	svc := &accountService{
		accountRepo: repo,
		logger:      zap.NewNop(),
	}

	limit := 0
	_, err := svc.UpdateAccount(context.Background(), 11, &UpdateAccountRequest{
		ConcurrentLimit: &limit,
	})

	assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
	if repo.updated != nil {
		t.Fatalf("invalid concurrent_limit should not be persisted")
	}
}

func TestUpdateAccountRejectsDuplicateGuildChannel(t *testing.T) {
	repo := &fakeAccountRepo{
		account: &model.Account{
			ID:              11,
			GuildID:         "guild-1",
			ChannelID:       "channel-1",
			UserToken:       "token",
			ConcurrentLimit: 20,
		},
		existingByGuildChannel: &model.Account{
			ID:        12,
			GuildID:   "guild-2",
			ChannelID: "channel-2",
		},
	}
	svc := &accountService{
		accountRepo: repo,
		logger:      zap.NewNop(),
	}

	_, err := svc.UpdateAccount(context.Background(), 11, &UpdateAccountRequest{
		GuildID:   stringPtr(" guild-2 "),
		ChannelID: stringPtr(" channel-2 "),
	})

	assertAppErrorCode(t, err, apperrors.ErrCodeAccountAlreadyExists)
	if repo.updated != nil {
		t.Fatalf("duplicate update should not be persisted")
	}
}

func TestUpdateAccountRejectsBlankProvidedField(t *testing.T) {
	tests := []struct {
		name string
		req  UpdateAccountRequest
	}{
		{
			name: "empty guild id",
			req:  UpdateAccountRequest{GuildID: stringPtr("")},
		},
		{
			name: "blank guild id",
			req:  UpdateAccountRequest{GuildID: stringPtr("   ")},
		},
		{
			name: "empty channel id",
			req:  UpdateAccountRequest{ChannelID: stringPtr("")},
		},
		{
			name: "blank channel id",
			req:  UpdateAccountRequest{ChannelID: stringPtr("   ")},
		},
		{
			name: "empty user token",
			req:  UpdateAccountRequest{UserToken: stringPtr("")},
		},
		{
			name: "blank user token",
			req:  UpdateAccountRequest{UserToken: stringPtr("   ")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeAccountRepo{
				account: &model.Account{
					ID:              11,
					GuildID:         "guild-1",
					ChannelID:       "channel-1",
					UserToken:       "token",
					ConcurrentLimit: 20,
				},
			}
			svc := &accountService{
				accountRepo: repo,
				logger:      zap.NewNop(),
			}

			_, err := svc.UpdateAccount(context.Background(), 11, &tt.req)

			assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
			if repo.updated != nil {
				t.Fatalf("blank update should not be persisted")
			}
		})
	}
}

func TestDeleteAccountRejectsActiveJobs(t *testing.T) {
	repo := &fakeAccountRepo{
		account: &model.Account{
			ID:              11,
			GuildID:         "guild-1",
			ChannelID:       "channel-1",
			UserToken:       "token",
			CurrentJobs:     1,
			ConcurrentLimit: 20,
		},
	}
	svc := &accountService{
		accountRepo: repo,
		logger:      zap.NewNop(),
	}

	err := svc.DeleteAccount(context.Background(), 11)

	assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
	if repo.deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want 0", repo.deleteCalls)
	}
}

func stringPtr(value string) *string {
	return &value
}

func TestAccountServiceRejectsZeroAccountIDBeforeRepository(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, *accountService) error
	}{
		{
			name: "get",
			run: func(ctx context.Context, svc *accountService) error {
				_, err := svc.GetAccountByID(ctx, 0)
				return err
			},
		},
		{
			name: "update",
			run: func(ctx context.Context, svc *accountService) error {
				_, err := svc.UpdateAccount(ctx, 0, &UpdateAccountRequest{})
				return err
			},
		},
		{
			name: "delete",
			run: func(ctx context.Context, svc *accountService) error {
				return svc.DeleteAccount(ctx, 0)
			},
		},
		{
			name: "acquire",
			run: func(ctx context.Context, svc *accountService) error {
				_, err := svc.AcquireAccount(ctx, 0)
				return err
			},
		},
		{
			name: "set health",
			run: func(ctx context.Context, svc *accountService) error {
				return svc.SetAccountHealthy(ctx, 0, true, "")
			},
		},
		{
			name: "decrement jobs",
			run: func(ctx context.Context, svc *accountService) error {
				return svc.DecrementJobs(ctx, 0)
			},
		},
		{
			name: "record result",
			run: func(ctx context.Context, svc *accountService) error {
				return svc.RecordTaskResult(ctx, 0, true, "")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeAccountRepo{}
			svc := &accountService{
				accountRepo: repo,
				logger:      zap.NewNop(),
			}

			err := tt.run(context.Background(), svc)

			assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
			if repo.anyCallCount() != 0 {
				t.Fatalf("repository calls = %d, want 0", repo.anyCallCount())
			}
		})
	}
}

func assertAppErrorCode(t *testing.T, err error, code apperrors.ErrorCode) {
	t.Helper()

	var appErr *apperrors.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("error = %T %v, want AppError code %s", err, err, code)
	}
	if appErr.Code != code {
		t.Fatalf("code = %q, want %q", appErr.Code, code)
	}
}

type fakeAccountRepo struct {
	account                   *model.Account
	accounts                  []model.Account
	existingByGuildChannel    *model.Account
	created                   *model.Account
	updated                   *model.Account
	getByIDCalls              int
	acquireByIDCalls          int
	deleteCalls               int
	setHealthyCalls           int
	decrementJobsCalls        int
	recordTaskResultCalls     int
	getByGuildAndChannelCalls int
	resetRuntime              bool
}

func (r *fakeAccountRepo) Create(ctx context.Context, account *model.Account) error {
	r.created = account
	if account.ID == 0 {
		account.ID = 1
	}
	return nil
}

func (r *fakeAccountRepo) GetByID(ctx context.Context, id uint) (*model.Account, error) {
	r.getByIDCalls++
	if r.account == nil {
		return nil, nil
	}
	account := *r.account
	return &account, nil
}

func (r *fakeAccountRepo) AcquireAvailable(ctx context.Context) (*model.Account, error) {
	return nil, nil
}

func (r *fakeAccountRepo) AcquireByID(ctx context.Context, id uint) (*model.Account, error) {
	r.acquireByIDCalls++
	return nil, nil
}

func (r *fakeAccountRepo) DecrementJobs(ctx context.Context, id uint) error {
	r.decrementJobsCalls++
	return nil
}

func (r *fakeAccountRepo) List(ctx context.Context) ([]model.Account, error) {
	accounts := make([]model.Account, len(r.accounts))
	copy(accounts, r.accounts)
	return accounts, nil
}

func (r *fakeAccountRepo) UpdateConfig(ctx context.Context, account *model.Account, resetRuntime bool) error {
	r.updated = account
	r.resetRuntime = resetRuntime
	return nil
}

func (r *fakeAccountRepo) Delete(ctx context.Context, id uint) error {
	r.deleteCalls++
	return nil
}

func (r *fakeAccountRepo) SetAccountHealthy(ctx context.Context, id uint, isHealthy bool, lastError string) error {
	r.setHealthyCalls++
	return nil
}

func (r *fakeAccountRepo) RecordTaskResult(ctx context.Context, id uint, success bool, lastError string) error {
	r.recordTaskResultCalls++
	return nil
}

func (r *fakeAccountRepo) GetByGuildAndChannel(ctx context.Context, guildID, channelID string) (*model.Account, error) {
	r.getByGuildAndChannelCalls++
	if r.existingByGuildChannel == nil {
		return nil, nil
	}
	if r.existingByGuildChannel.GuildID != guildID || r.existingByGuildChannel.ChannelID != channelID {
		return nil, nil
	}
	account := *r.existingByGuildChannel
	return &account, nil
}

func (r *fakeAccountRepo) anyCallCount() int {
	return r.getByIDCalls +
		r.acquireByIDCalls +
		r.deleteCalls +
		r.setHealthyCalls +
		r.decrementJobsCalls +
		r.recordTaskResultCalls +
		r.getByGuildAndChannelCalls
}
