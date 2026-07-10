package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/pkg/constants"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestAccountRepositoryRejectsInvalidInputBeforeDatabase(t *testing.T) {
	repo := &accountRepository{}
	ctx := context.Background()

	validAccount := func() *model.Account {
		return &model.Account{
			ID:              1,
			GuildID:         "guild-1",
			ChannelID:       "channel-1",
			UserToken:       "token",
			ConcurrentLimit: constants.DefaultConcurrentLimit,
		}
	}

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "create nil account",
			run: func() error {
				return repo.Create(ctx, nil)
			},
		},
		{
			name: "create blank guild",
			run: func() error {
				account := validAccount()
				account.GuildID = "   "
				return repo.Create(ctx, account)
			},
		},
		{
			name: "create blank channel",
			run: func() error {
				account := validAccount()
				account.ChannelID = "   "
				return repo.Create(ctx, account)
			},
		},
		{
			name: "create blank token",
			run: func() error {
				account := validAccount()
				account.UserToken = "   "
				return repo.Create(ctx, account)
			},
		},
		{
			name: "create overlong guild",
			run: func() error {
				account := validAccount()
				account.GuildID = strings.Repeat("g", constants.MaxAccountGuildIDLength+1)
				return repo.Create(ctx, account)
			},
		},
		{
			name: "create overlong channel",
			run: func() error {
				account := validAccount()
				account.ChannelID = strings.Repeat("c", constants.MaxAccountChannelIDLength+1)
				return repo.Create(ctx, account)
			},
		},
		{
			name: "create overlong token",
			run: func() error {
				account := validAccount()
				account.UserToken = strings.Repeat("t", constants.MaxAccountUserTokenLength+1)
				return repo.Create(ctx, account)
			},
		},
		{
			name: "create invalid concurrent limit",
			run: func() error {
				account := validAccount()
				account.ConcurrentLimit = 0
				return repo.Create(ctx, account)
			},
		},
		{
			name: "create negative current jobs",
			run: func() error {
				account := validAccount()
				account.CurrentJobs = -1
				return repo.Create(ctx, account)
			},
		},
		{
			name: "create negative error count",
			run: func() error {
				account := validAccount()
				account.ErrorCount = -1
				return repo.Create(ctx, account)
			},
		},
		{
			name: "create negative success count",
			run: func() error {
				account := validAccount()
				account.SuccessCount = -1
				return repo.Create(ctx, account)
			},
		},
		{
			name: "get zero id",
			run: func() error {
				_, err := repo.GetByID(ctx, 0)
				return err
			},
		},
		{
			name: "acquire zero id",
			run: func() error {
				_, err := repo.AcquireByID(ctx, 0)
				return err
			},
		},
		{
			name: "decrement zero id",
			run: func() error {
				return repo.DecrementJobs(ctx, 0)
			},
		},
		{
			name: "update nil account",
			run: func() error {
				return repo.UpdateConfig(ctx, nil, false)
			},
		},
		{
			name: "update zero id",
			run: func() error {
				account := validAccount()
				account.ID = 0
				return repo.UpdateConfig(ctx, account, false)
			},
		},
		{
			name: "update overlong token",
			run: func() error {
				account := validAccount()
				account.UserToken = strings.Repeat("t", constants.MaxAccountUserTokenLength+1)
				return repo.UpdateConfig(ctx, account, false)
			},
		},
		{
			name: "delete zero id",
			run: func() error {
				return repo.Delete(ctx, 0)
			},
		},
		{
			name: "set health zero id",
			run: func() error {
				return repo.SetAccountHealthy(ctx, 0, true, "")
			},
		},
		{
			name: "record result zero id",
			run: func() error {
				return repo.RecordTaskResult(ctx, 0, true, "")
			},
		},
		{
			name: "get by blank guild",
			run: func() error {
				_, err := repo.GetByGuildAndChannel(ctx, "   ", "channel-1")
				return err
			},
		},
		{
			name: "get by blank channel",
			run: func() error {
				_, err := repo.GetByGuildAndChannel(ctx, "guild-1", "   ")
				return err
			},
		},
		{
			name: "get by overlong guild",
			run: func() error {
				_, err := repo.GetByGuildAndChannel(ctx, strings.Repeat("g", constants.MaxAccountGuildIDLength+1), "channel-1")
				return err
			},
		},
		{
			name: "get by overlong channel",
			run: func() error {
				_, err := repo.GetByGuildAndChannel(ctx, "guild-1", strings.Repeat("c", constants.MaxAccountChannelIDLength+1))
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertRepositoryAppErrorCode(t, tt.run(), apperrors.ErrCodeInvalidInput)
		})
	}
}

func TestValidateAccountForWriteTrimsFields(t *testing.T) {
	account := &model.Account{
		GuildID:         "  guild-1  ",
		ChannelID:       "  channel-1  ",
		UserToken:       "  token  ",
		ConcurrentLimit: constants.DefaultConcurrentLimit,
	}

	if err := validateAccountForWrite(account); err != nil {
		t.Fatalf("validateAccountForWrite returned error: %v", err)
	}
	if account.GuildID != "guild-1" {
		t.Fatalf("guild_id = %q, want trimmed guild-1", account.GuildID)
	}
	if account.ChannelID != "channel-1" {
		t.Fatalf("channel_id = %q, want trimmed channel-1", account.ChannelID)
	}
	if account.UserToken != "token" {
		t.Fatalf("user_token = %q, want trimmed token", account.UserToken)
	}
}

func TestAccountConfigUpdatesExcludeRuntimeCounters(t *testing.T) {
	account := &model.Account{
		GuildID:         "guild-1",
		ChannelID:       "channel-1",
		UserToken:       "token",
		IsDisabled:      true,
		IsHealthy:       false,
		ConcurrentLimit: 30,
		CurrentJobs:     7,
		SuccessCount:    9,
		LastError:       strings.Repeat("x", maxAccountLastErrorLength+1),
	}

	updates := accountConfigUpdates(account, false)

	for _, key := range []string{
		"is_healthy",
		"current_jobs",
		"error_count",
		"success_count",
		"last_error",
		"last_used_at",
		"created_at",
		"deleted_at",
	} {
		if _, ok := updates[key]; ok {
			t.Fatalf("accountConfigUpdates should not include runtime field %q", key)
		}
	}

	for _, key := range []string{
		"guild_id",
		"channel_id",
		"user_token",
		"is_disabled",
		"concurrent_limit",
		"updated_at",
	} {
		if _, ok := updates[key]; !ok {
			t.Fatalf("accountConfigUpdates missing field %q", key)
		}
	}
}

func TestAccountConfigUpdatesCanExplicitlyResetRuntimeHealth(t *testing.T) {
	account := &model.Account{
		GuildID:         "guild-1",
		ChannelID:       "channel-1",
		UserToken:       "token",
		IsDisabled:      false,
		IsHealthy:       true,
		ConcurrentLimit: 30,
		ErrorCount:      2,
		LastError:       "previous failure",
	}

	updates := accountConfigUpdates(account, true)

	if updates["is_healthy"] != false {
		t.Fatalf("is_healthy = %v, want false", updates["is_healthy"])
	}
	if updates["error_count"] != 0 {
		t.Fatalf("error_count = %v, want 0", updates["error_count"])
	}
	if updates["last_error"] != "" {
		t.Fatalf("last_error = %q, want empty", updates["last_error"])
	}
}

func TestAccountConfigUpdateQueryRequiresIdleOnlyForRuntimeReset(t *testing.T) {
	db := newDryRunAccountDB(t)
	account := &model.Account{
		ID:              11,
		GuildID:         "guild-1",
		ChannelID:       "channel-1",
		UserToken:       "token",
		ConcurrentLimit: constants.DefaultConcurrentLimit,
	}

	resetWhere := queryWhereClauseText(accountConfigUpdateQuery(db.Session(&gorm.Session{DryRun: true}), account.ID, true))
	if !strings.Contains(resetWhere, "current_jobs") {
		t.Fatalf("runtime reset update WHERE = %q, want current_jobs guard", resetWhere)
	}

	limitOnlyWhere := queryWhereClauseText(accountConfigUpdateQuery(db.Session(&gorm.Session{DryRun: true}), account.ID, false))
	if strings.Contains(limitOnlyWhere, "current_jobs") {
		t.Fatalf("limit-only update WHERE = %q, want no current_jobs guard", limitOnlyWhere)
	}
}

func TestIdleAccountDeleteQueryRequiresNoCurrentJobs(t *testing.T) {
	db := newDryRunAccountDB(t)

	where := queryWhereClauseText(idleAccountDeleteQuery(db.Session(&gorm.Session{DryRun: true}), 11))

	for _, expected := range []string{"id", "current_jobs"} {
		if !strings.Contains(where, expected) {
			t.Fatalf("delete WHERE = %q, want %q guard", where, expected)
		}
	}
}

func TestAccountRepositoryRejectsMissingDatabase(t *testing.T) {
	repo := &accountRepository{}
	ctx := context.Background()
	validAccount := func() *model.Account {
		return &model.Account{
			ID:              11,
			GuildID:         "guild-1",
			ChannelID:       "channel-1",
			UserToken:       "token",
			ConcurrentLimit: constants.DefaultConcurrentLimit,
		}
	}

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "create",
			run: func() error {
				account := validAccount()
				account.ID = 0
				return repo.Create(ctx, account)
			},
		},
		{
			name: "get",
			run: func() error {
				_, err := repo.GetByID(ctx, 11)
				return err
			},
		},
		{
			name: "acquire available",
			run: func() error {
				_, err := repo.AcquireAvailable(ctx)
				return err
			},
		},
		{
			name: "acquire by id",
			run: func() error {
				_, err := repo.AcquireByID(ctx, 11)
				return err
			},
		},
		{
			name: "decrement jobs",
			run: func() error {
				return repo.DecrementJobs(ctx, 11)
			},
		},
		{
			name: "list",
			run: func() error {
				_, err := repo.List(ctx)
				return err
			},
		},
		{
			name: "update",
			run: func() error {
				return repo.UpdateConfig(ctx, validAccount(), false)
			},
		},
		{
			name: "delete",
			run: func() error {
				return repo.Delete(ctx, 11)
			},
		},
		{
			name: "set health",
			run: func() error {
				return repo.SetAccountHealthy(ctx, 11, true, "")
			},
		},
		{
			name: "record task result",
			run: func() error {
				return repo.RecordTaskResult(ctx, 11, false, "failed")
			},
		},
		{
			name: "get by guild and channel",
			run: func() error {
				_, err := repo.GetByGuildAndChannel(ctx, "guild-1", "channel-1")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertRepositoryAppErrorCode(t, tt.run(), apperrors.ErrCodeDatabaseError)
		})
	}
}

func TestAccountRepositoryNilReceiverRejectsMissingDatabase(t *testing.T) {
	var repo *accountRepository

	_, err := repo.GetByID(context.Background(), 11)

	assertRepositoryAppErrorCode(t, err, apperrors.ErrCodeDatabaseError)
}

func TestAccountUpdateResultError(t *testing.T) {
	tests := []struct {
		name        string
		result      *gorm.DB
		wantCode    apperrors.ErrorCode
		wantNoError bool
	}{
		{
			name: "success",
			result: &gorm.DB{
				RowsAffected: 1,
			},
			wantNoError: true,
		},
		{
			name: "zero rows",
			result: &gorm.DB{
				RowsAffected: 0,
			},
			wantCode: apperrors.ErrCodeAccountNotFound,
		},
		{
			name: "record not found",
			result: &gorm.DB{
				Error: gorm.ErrRecordNotFound,
			},
			wantCode: apperrors.ErrCodeAccountNotFound,
		},
		{
			name: "database error",
			result: &gorm.DB{
				Error: errors.New("db down"),
			},
			wantCode: apperrors.ErrCodeDatabaseError,
		},
		{
			name:     "nil result",
			result:   nil,
			wantCode: apperrors.ErrCodeDatabaseError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := accountUpdateResultError(tt.result, 11)
			if tt.wantNoError {
				if err != nil {
					t.Fatalf("accountUpdateResultError returned error: %v", err)
				}
				return
			}
			assertRepositoryAppErrorCode(t, err, tt.wantCode)
		})
	}
}

func TestAccountUpdateResultErrorWithActiveGuard(t *testing.T) {
	repo := &accountRepository{}
	ctx := context.Background()

	if err := repo.accountUpdateResultErrorWithActiveGuard(ctx, &gorm.DB{RowsAffected: 1}, 11, accountDeleteActiveJobsMessage); err != nil {
		t.Fatalf("accountUpdateResultErrorWithActiveGuard returned error: %v", err)
	}

	err := repo.accountUpdateResultErrorWithActiveGuard(ctx, nil, 11, accountDeleteActiveJobsMessage)
	assertRepositoryAppErrorCode(t, err, apperrors.ErrCodeDatabaseError)

	err = repo.accountUpdateResultErrorWithActiveGuard(ctx, &gorm.DB{Error: gorm.ErrRecordNotFound}, 11, accountDeleteActiveJobsMessage)
	assertRepositoryAppErrorCode(t, err, apperrors.ErrCodeAccountNotFound)

	err = repo.accountNoRowsError(ctx, 11, "")
	assertRepositoryAppErrorCode(t, err, apperrors.ErrCodeAccountNotFound)
}

func TestTaskResultUpdatesSuccessMarksAccountHealthy(t *testing.T) {
	now := time.Now()

	updates := taskResultUpdates(true, "old error", now)

	if updates["last_used_at"] != now {
		t.Fatalf("last_used_at = %v, want %v", updates["last_used_at"], now)
	}
	healthyExpr, ok := updates["is_healthy"].(clause.Expr)
	if !ok {
		t.Fatalf("is_healthy update type = %T, want clause.Expr", updates["is_healthy"])
	}
	if healthyExpr.SQL != "CASE WHEN is_disabled THEN FALSE ELSE TRUE END" {
		t.Fatalf("is_healthy SQL = %q, want disabled-safe expression", healthyExpr.SQL)
	}
	if updates["error_count"] != 0 {
		t.Fatalf("error_count = %v, want 0", updates["error_count"])
	}
	if updates["last_error"] != "" {
		t.Fatalf("last_error = %q, want empty", updates["last_error"])
	}

	successExpr, ok := updates["success_count"].(clause.Expr)
	if !ok {
		t.Fatalf("success_count update type = %T, want clause.Expr", updates["success_count"])
	}
	if successExpr.SQL != "success_count + ?" {
		t.Fatalf("success_count SQL = %q, want increment expression", successExpr.SQL)
	}
}

func TestTaskResultUpdatesFailureMarksUnhealthyAtomically(t *testing.T) {
	updates := taskResultUpdates(false, "discord failed", time.Now())

	if updates["last_error"] != "discord failed" {
		t.Fatalf("last_error = %q, want discord failed", updates["last_error"])
	}

	errorExpr, ok := updates["error_count"].(clause.Expr)
	if !ok {
		t.Fatalf("error_count update type = %T, want clause.Expr", updates["error_count"])
	}
	if errorExpr.SQL != "error_count + ?" {
		t.Fatalf("error_count SQL = %q, want increment expression", errorExpr.SQL)
	}

	healthyExpr, ok := updates["is_healthy"].(clause.Expr)
	if !ok {
		t.Fatalf("is_healthy update type = %T, want clause.Expr", updates["is_healthy"])
	}
	if healthyExpr.SQL != "CASE WHEN is_disabled THEN FALSE WHEN error_count + ? >= ? THEN FALSE ELSE is_healthy END" {
		t.Fatalf("is_healthy SQL = %q, want threshold CASE expression", healthyExpr.SQL)
	}
	if len(healthyExpr.Vars) != 2 {
		t.Fatalf("is_healthy vars len = %d, want 2", len(healthyExpr.Vars))
	}
	if healthyExpr.Vars[0] != 1 || healthyExpr.Vars[1] != constants.MaxErrorCount {
		t.Fatalf("is_healthy vars = %#v, want increment and max error count", healthyExpr.Vars)
	}
}

func TestTaskResultUpdatesFailureTruncatesLastError(t *testing.T) {
	longError := strings.Repeat("x", maxAccountLastErrorLength+20)

	updates := taskResultUpdates(false, longError, time.Now())

	lastError, ok := updates["last_error"].(string)
	if !ok {
		t.Fatalf("last_error update type = %T, want string", updates["last_error"])
	}
	if len(lastError) != maxAccountLastErrorLength {
		t.Fatalf("last_error len = %d, want %d", len(lastError), maxAccountLastErrorLength)
	}
	if lastError != strings.Repeat("x", maxAccountLastErrorLength) {
		t.Fatalf("last_error was not truncated to expected prefix")
	}
}

func TestAccountJobDecrementValueNeverGoesBelowZero(t *testing.T) {
	value := accountJobDecrementValue()

	expr, ok := value.(clause.Expr)
	if !ok {
		t.Fatalf("accountJobDecrementValue = %T, want clause.Expr", value)
	}
	if expr.SQL != "CASE WHEN current_jobs > 0 THEN current_jobs - ? ELSE 0 END" {
		t.Fatalf("SQL = %q, want non-negative decrement expression", expr.SQL)
	}
	if len(expr.Vars) != 1 || expr.Vars[0] != 1 {
		t.Fatalf("vars = %#v, want [1]", expr.Vars)
	}
}

func TestAccountJobIncrementQueryRequiresAvailableAccount(t *testing.T) {
	db := newDryRunAccountDB(t)

	where := queryWhereClauseText(accountJobIncrementQuery(db.Session(&gorm.Session{DryRun: true}), 11))

	for _, expected := range []string{"id", "is_disabled", "is_healthy", "current_jobs"} {
		if !strings.Contains(where, expected) {
			t.Fatalf("account acquire WHERE = %q, want %q guard", where, expected)
		}
	}
	if !strings.Contains(where, "concurrent_limit") {
		t.Fatalf("account acquire WHERE = %q, want concurrent_limit guard", where)
	}
}

func TestAccountJobIncrementValueAddsOneSlot(t *testing.T) {
	value := accountJobIncrementValue()

	expr, ok := value.(clause.Expr)
	if !ok {
		t.Fatalf("accountJobIncrementValue = %T, want clause.Expr", value)
	}
	if expr.SQL != "current_jobs + ?" {
		t.Fatalf("SQL = %q, want increment expression", expr.SQL)
	}
	if len(expr.Vars) != 1 || expr.Vars[0] != 1 {
		t.Fatalf("vars = %#v, want [1]", expr.Vars)
	}
}

func TestAcquireAccountSlotRejectsMissingAccount(t *testing.T) {
	err := acquireAccountSlot(nil, nil)

	assertRepositoryAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
}

func TestTaskResultUpdatesFailureRedactsLastError(t *testing.T) {
	updates := taskResultUpdates(false,
		`discord API error: user_token="secret-token" callback=https://user:pass@example.com/hook?token=secret#frag`,
		time.Now(),
	)

	lastError, ok := updates["last_error"].(string)
	if !ok {
		t.Fatalf("last_error update type = %T, want string", updates["last_error"])
	}
	for _, forbidden := range []string{"secret-token", "token=secret", "user:pass", "#frag"} {
		if strings.Contains(lastError, forbidden) {
			t.Fatalf("last_error exposed %q: %s", forbidden, lastError)
		}
	}
	if !strings.Contains(lastError, `user_token="<redacted>"`) || !strings.Contains(lastError, "https://example.com/hook") {
		t.Fatalf("last_error did not keep useful redacted context: %s", lastError)
	}
}

func TestSanitizeAccountLastErrorUsesRuneBoundaries(t *testing.T) {
	exact := strings.Repeat("好", maxAccountLastErrorLength)
	if got := sanitizeAccountLastError(exact); got != exact {
		t.Fatalf("exact length error was truncated")
	}

	tooLong := exact + "坏"
	got := sanitizeAccountLastError(tooLong)
	if got != exact {
		t.Fatalf("sanitizeAccountLastError returned unexpected value")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("sanitizeAccountLastError returned invalid UTF-8")
	}
	if utf8.RuneCountInString(got) != maxAccountLastErrorLength {
		t.Fatalf("rune count = %d, want %d", utf8.RuneCountInString(got), maxAccountLastErrorLength)
	}
}

func TestAccountHealthUpdateValueNeverMarksDisabledAccountHealthy(t *testing.T) {
	value := accountHealthUpdateValue(true)
	expr, ok := value.(clause.Expr)
	if !ok {
		t.Fatalf("accountHealthUpdateValue(true) = %T, want clause.Expr", value)
	}
	if expr.SQL != "CASE WHEN is_disabled THEN FALSE ELSE TRUE END" {
		t.Fatalf("SQL = %q, want disabled-safe expression", expr.SQL)
	}

	if value := accountHealthUpdateValue(false); value != false {
		t.Fatalf("accountHealthUpdateValue(false) = %#v, want false", value)
	}
}

func TestAccountWriteErrorMapsActiveGuildChannelUniqueViolation(t *testing.T) {
	account := &model.Account{GuildID: "guild-1", ChannelID: "channel-1"}
	err := accountWriteError(&pgconn.PgError{
		Code:           "23505",
		ConstraintName: AccountActiveGuildChannelIndexName,
	}, account)

	var appErr *apperrors.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("error type = %T, want AppError", err)
	}
	if appErr.Code != apperrors.ErrCodeAccountAlreadyExists {
		t.Fatalf("error code = %s, want %s", appErr.Code, apperrors.ErrCodeAccountAlreadyExists)
	}
}

func TestAccountWriteErrorKeepsOtherDatabaseErrors(t *testing.T) {
	err := accountWriteError(&pgconn.PgError{
		Code:           "23505",
		ConstraintName: "other_constraint",
	}, &model.Account{})

	var appErr *apperrors.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("error type = %T, want AppError", err)
	}
	if appErr.Code != apperrors.ErrCodeDatabaseError {
		t.Fatalf("error code = %s, want %s", appErr.Code, apperrors.ErrCodeDatabaseError)
	}
}

func newDryRunAccountDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=localhost user=test dbname=test sslmode=disable",
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	if err != nil {
		t.Fatalf("failed to open dry-run database: %v", err)
	}
	return db
}

func queryWhereClauseText(query *gorm.DB) string {
	if query == nil || query.Statement == nil {
		return ""
	}
	where, ok := query.Statement.Clauses["WHERE"]
	if !ok {
		return ""
	}
	return fmt.Sprint(where.Expression)
}
