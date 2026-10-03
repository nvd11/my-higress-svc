package db

import (
	"context"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/config"
)

var DB *sqlx.DB

type LLMRequestLog struct {
	ID               string    `db:"id" json:"id"`
	RequestID        string    `db:"request_id" json:"request_id"`
	APIKeyAlias      string    `db:"api_key_alias" json:"api_key_alias"`
	ModelRequested   string    `db:"model_requested" json:"model_requested"`
	ModelUsed        string    `db:"model_used" json:"model_used"`
	Provider         string    `db:"provider" json:"provider"`
	ProviderKeyAlias string    `db:"provider_key_alias" json:"provider_key_alias"`
	PromptTokens     int       `db:"prompt_tokens" json:"prompt_tokens"`
	CompletionTokens int       `db:"completion_tokens" json:"completion_tokens"`
	ReasoningTokens  int       `db:"reasoning_tokens" json:"reasoning_tokens"`
	TotalTokens      int       `db:"total_tokens" json:"total_tokens"`
	CostUSD          float64   `db:"cost_usd" json:"cost_usd"`
	CostCNY          float64   `db:"cost_cny" json:"cost_cny"`
	FxRate           float64   `db:"fx_rate" json:"fx_rate"`
	LatencyMS        int       `db:"latency_ms" json:"latency_ms"`
	StatusCode       int       `db:"status_code" json:"status_code"`
	ErrorMsg         *string   `db:"error_msg" json:"error_msg"`
	CreatedAt        time.Time `db:"created_at" json:"created_at"`
}

func InitDB(cfg *config.Config) (*sqlx.DB, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.MySQLUser,
		cfg.MySQLPassword,
		cfg.MySQLHost,
		cfg.MySQLPort,
		cfg.MySQLDatabase,
	)

	db, err := sqlx.Connect("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed connecting to MySQL: %w", err)
	}

	db.SetMaxOpenConns(50)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(10 * time.Minute)

	DB = db
	return db, nil
}

func CheckHealth(ctx context.Context) error {
	if DB == nil {
		return fmt.Errorf("database not initialized")
	}
	return DB.PingContext(ctx)
}
