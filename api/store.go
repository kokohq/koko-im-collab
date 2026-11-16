package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
)

//go:embed db/schema.sql
var dbSchemaSQL string

const dbTimeout = 5 * time.Second

var (
	dataKeyOnce    sync.Once
	dataKey        []byte
	dataKeyErr     error
	defaultDataKey = []byte("koko-default-encryption-key-32bx")
)

type PlanTier struct {
	ID                    int64     `json:"id"`
	TierCode              string    `json:"tier_code"`
	DisplayName           string    `json:"display_name"`
	MonthlyPriceUSD       float64   `json:"monthly_price_usd"`
	StorageBytes          int64     `json:"storage_bytes"`
	MonthlySummarizeLimit int       `json:"monthly_summarize_limit"`
	MonthlyAnalyzeLimit   int       `json:"monthly_analyze_limit"`
	MonthlyActLimit       int       `json:"monthly_act_limit"`
	CreatedAt             time.Time `json:"created_at"`
}

type Account struct {
	AccountID            string    `json:"account_id"`
	DisplayName          string    `json:"display_name,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	SubscribedPlanTierID int64     `json:"subscribed_plan_tier_id"`
	TotalStorageUsed     int64     `json:"total_storage_used"`
}

type Group struct {
	ID            int64     `json:"id"`
	GroupID       string    `json:"group_id"`
	AccountID     string    `json:"account_id"`
	GroupName     string    `json:"group_name"`
	MessengerType string    `json:"messenger_type"`
	AddedAt       time.Time `json:"added_at"`
}

type User struct {
	ID              int64     `json:"id"`
	AccountID       string    `json:"account_id"`
	UserName        string    `json:"user_name"`
	MessengerType   string    `json:"messenger_type"`
	MessengerChatID string    `json:"messenger_chat_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type GroupMembership struct {
	GroupID int64     `json:"group_id"`
	UserID  int64     `json:"user_id"`
	AddedAt time.Time `json:"added_at"`
}

type GroupMemberDestination struct {
	MessengerType string `json:"messenger_type"`
	ChatID        string `json:"chat_id"`
}

type PlanChangeRequest struct {
	ID                  int64     `json:"id"`
	AccountID           string    `json:"account_id"`
	RequestedByUserID   *int64    `json:"requested_by_user_id,omitempty"`
	RequestedByUserName string    `json:"requested_by_user_name"`
	CurrentPlanTierID   int64     `json:"current_plan_tier_id"`
	RequestedPlanTierID int64     `json:"requested_plan_tier_id"`
	CreatedAt           time.Time `json:"created_at"`
}

type MessengerInput struct {
	ID            int64           `json:"id"`
	AccountID     string          `json:"account_id"`
	GroupName     string          `json:"group_name"`
	UserName      string          `json:"user_name"`
	MessengerType string          `json:"messenger_type"`
	ChatID        string          `json:"chat_id"`
	MessageID     string          `json:"message_id"`
	MessageType   string          `json:"message_type"`
	TextContent   string          `json:"text_content"`
	MediaID       string          `json:"media_id"`
	MimeType      string          `json:"mime_type"`
	Payload       json.RawMessage `json:"payload"`
	SentAt        time.Time       `json:"sent_at"`     // original message timestamp from sender platform (UTC)
	ReceivedAt    time.Time       `json:"received_at"` // server ingest time
}

// EffectiveTime returns SentAt when available (non-zero), otherwise ReceivedAt.
// Use this for prompt timelines and ordering instead of ReceivedAt alone.
func (m MessengerInput) EffectiveTime() time.Time {
	if !m.SentAt.IsZero() {
		return m.SentAt.UTC()
	}
	return m.ReceivedAt.UTC()
}

type MessengerSummary struct {
	ID                 int64     `json:"id"`
	AccountID          string    `json:"account_id"`
	MessengerType      string    `json:"messenger_type"`
	ChatID             string    `json:"chat_id"`
	Domain             string    `json:"domain"`
	WindowStart        time.Time `json:"window_start"`
	WindowEnd          time.Time `json:"window_end"`
	SourceMessageCount int       `json:"source_message_count"`
	SummaryText        string    `json:"summary_text"`
	SummarizedAt       time.Time `json:"summarized_at"`
}

type PolicyAuditLog struct {
	ID             int64           `json:"id"`
	AccountID      string          `json:"account_id"`
	GroupName      string          `json:"group_name"`
	UserName       string          `json:"user_name"`
	MessengerType  string          `json:"messenger_type"`
	ChatID         string          `json:"chat_id"`
	MessageID      string          `json:"message_id"`
	MessageType    string          `json:"message_type"`
	Action         string          `json:"action"`
	ReasonCode     string          `json:"reason_code"`
	WarningMessage string          `json:"warning_message"`
	Details        json.RawMessage `json:"details"`
	CreatedAt      time.Time       `json:"created_at"`
}

// ============ USP Feature Models ============

type MessageSentiment struct {
	ID             int64          `json:"id"`
	AccountID      string         `json:"account_id"`
	GroupName      string         `json:"group_name"`
	MessageID      string         `json:"message_id"`
	SentimentScore float64        `json:"sentiment_score"` // -1.0 to 1.0
	SentimentLabel string         `json:"sentiment_label"`
	EmotionTags    pq.StringArray `json:"emotion_tags"`
	Confidence     float64        `json:"confidence"`
	AnalyzedAt     time.Time      `json:"analyzed_at"`
}

type MessageTopic struct {
	ID             int64     `json:"id"`
	AccountID      string    `json:"account_id"`
	GroupName      string    `json:"group_name"`
	MessageID      string    `json:"message_id"`
	TopicName      string    `json:"topic_name"`
	TopicCategory  string    `json:"topic_category"`
	RelevanceScore float64   `json:"relevance_score"`
	TaggedAt       time.Time `json:"tagged_at"`
}

type ExtractedTodo struct {
	ID                int64          `json:"id"`
	AccountID         string         `json:"account_id"`
	GroupName         string         `json:"group_name"`
	MessageID         string         `json:"message_id"`
	ExtractedFromUser string         `json:"extracted_from_user"`
	TodoText          string         `json:"todo_text"`
	AssignedTo        string         `json:"assigned_to"`
	DueDate           *time.Time     `json:"due_date"`
	Priority          string         `json:"priority"`
	Status            string         `json:"status"`
	Confidence        float64        `json:"confidence"`
	Tags              pq.StringArray `json:"tags"`
	ExtractedAt       time.Time      `json:"extracted_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

type KnowledgeBaseArticle struct {
	ID             int64          `json:"id"`
	AccountID      string         `json:"account_id"`
	GroupName      string         `json:"group_name"`
	Title          string         `json:"title"`
	Content        string         `json:"content"`
	AutoGenerated  bool           `json:"auto_generated"`
	SourceMessages pq.StringArray `json:"source_messages"`
	Category       string         `json:"category"`
	Tags           pq.StringArray `json:"tags"`
	ViewCount      int            `json:"view_count"`
	HelpfulCount   int            `json:"helpful_count"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type GroupAnalytics struct {
	ID                 int64          `json:"id"`
	AccountID          string         `json:"account_id"`
	GroupName          string         `json:"group_name"`
	AnalysisDate       time.Time      `json:"analysis_date"`
	MessageCount       int            `json:"message_count"`
	UniqueParticipants int            `json:"unique_participants"`
	AvgSentimentScore  float64        `json:"avg_sentiment_score"`
	DominantSentiment  string         `json:"dominant_sentiment"`
	TopTopics          pq.StringArray `json:"top_topics"`
	TodoCount          int            `json:"todo_count"`
	TodoCompleted      int            `json:"todo_completed"`
	EngagementScore    float64        `json:"engagement_score"`
	HealthScore        float64        `json:"health_score"`
	CreatedAt          time.Time      `json:"created_at"`
}

type UserPreferences struct {
	ID                      int64     `json:"id"`
	AccountID               string    `json:"account_id"`
	UserName                string    `json:"user_name"`
	PreferredLanguage       string    `json:"preferred_language"`
	EmailDigestFrequency    string    `json:"email_digest_frequency"`
	EnableSentimentAnalysis bool      `json:"enable_sentiment_analysis"`
	EnableTopicExtraction   bool      `json:"enable_topic_extraction"`
	EnableTodoExtraction    bool      `json:"enable_todo_extraction"`
	EnableVoiceSummaries    bool      `json:"enable_voice_summaries"`
	DarkMode                bool      `json:"dark_mode"`
	Timezone                string    `json:"timezone"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

type ConversationTimeline struct {
	ID               int64          `json:"id"`
	AccountID        string         `json:"account_id"`
	GroupName        string         `json:"group_name"`
	EventTime        time.Time      `json:"event_time"`
	EventType        string         `json:"event_type"`
	EventDescription string         `json:"event_description"`
	SourceMessageIDs pq.StringArray `json:"source_message_ids"`
	SentimentContext string         `json:"sentiment_context"`
	CreatedAt        time.Time      `json:"created_at"`
}

type NLPAcronymExpansion struct {
	ID           int64     `json:"id"`
	AccountID    string    `json:"account_id"`
	GroupName    string    `json:"group_name"`
	Domain       string    `json:"domain"`
	Locale       string    `json:"locale"`
	ShortForm    string    `json:"short_form"`
	ExpandedForm string    `json:"expanded_form"`
	Source       string    `json:"source"`
	LearnedAt    time.Time `json:"learned_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type NLPPhraseRewrite struct {
	ID               int64     `json:"id"`
	AccountID        string    `json:"account_id"`
	GroupName        string    `json:"group_name"`
	Domain           string    `json:"domain"`
	Locale           string    `json:"locale"`
	SourcePhrase     string    `json:"source_phrase"`
	NormalizedPhrase string    `json:"normalized_phrase"`
	Source           string    `json:"source"`
	LearnedAt        time.Time `json:"learned_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type AppStore struct {
	db *sql.DB

	mu          sync.RWMutex
	planTiers   map[int64]PlanTier
	accounts    map[string]Account
	groups      map[int64]Group
	users       map[int64]User
	memberships map[string]GroupMembership
	nlpAcronyms map[string]NLPAcronymExpansion
	nlpPhrases  map[string]NLPPhraseRewrite
}

func getDataEncryptionKey() ([]byte, error) {
	dataKeyOnce.Do(func() {
		raw := stringsTrimmed(os.Getenv("KOKO_DATA_ENCRYPTION_KEY"))
		if raw == "" {
			log.Printf("KOKO_DATA_ENCRYPTION_KEY is not set; using built-in fallback key. Set env var in production.")
			dataKey = append([]byte(nil), defaultDataKey...)
			return
		}

		if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
			if len(decoded) == 32 {
				dataKey = decoded
				return
			}
		}

		if len(raw) == 32 {
			dataKey = []byte(raw)
			return
		}

		dataKeyErr = fmt.Errorf("KOKO_DATA_ENCRYPTION_KEY must be base64-encoded 32 bytes or raw 32-character key")
	})
	if dataKeyErr != nil {
		return nil, dataKeyErr
	}
	return dataKey, nil
}

func encryptBytesValue(plain []byte) (string, error) {
	if len(plain) == 0 {
		return "", nil
	}
	key, err := getDataEncryptionKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, plain, nil)
	payload := append(nonce, ciphertext...)
	return base64.StdEncoding.EncodeToString(payload), nil
}

func decryptBytesValue(cipherTextB64 string) ([]byte, error) {
	if stringsTrimmed(cipherTextB64) == "" {
		return nil, nil
	}
	key, err := getDataEncryptionKey()
	if err != nil {
		return nil, err
	}
	payload, err := base64.StdEncoding.DecodeString(cipherTextB64)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(payload) < gcm.NonceSize() {
		return nil, fmt.Errorf("invalid encrypted payload")
	}
	nonce, ciphertext := payload[:gcm.NonceSize()], payload[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}
	return plain, nil
}

func encryptStringValue(plain string) (string, error) {
	if stringsTrimmed(plain) == "" {
		return "", nil
	}
	return encryptBytesValue([]byte(plain))
}

func decryptStringValue(cipherText string) (string, error) {
	plain, err := decryptBytesValue(cipherText)
	if err != nil {
		// Backward compatibility: pre-encryption rows may still contain plaintext.
		return cipherText, nil
	}
	return string(plain), nil
}

func newSeededInMemoryStore() *AppStore {
	store := &AppStore{
		planTiers:   map[int64]PlanTier{},
		accounts:    map[string]Account{},
		groups:      map[int64]Group{},
		users:       map[int64]User{},
		memberships: map[string]GroupMembership{},
		nlpAcronyms: map[string]NLPAcronymExpansion{},
		nlpPhrases:  map[string]NLPPhraseRewrite{},
	}
	if err := store.seedSampleData(context.Background()); err != nil {
		log.Printf("Sample data seed failed (continuing): %v", err)
	}
	return store
}

// applySchema splits the schema SQL on semicolons and executes each statement
// individually. Statements that fail because the object already exists
// (duplicate key / already exists errors) are skipped so that re-deploys on
// an existing database do not abort startup. Any other error causes the schema
// migration to be aborted and the error is returned.
func applySchema(ctx context.Context, db *sql.DB, schema string) error {
	// Strip single-line comments (-- ...) before splitting on semicolons.
	// This prevents inline comments that contain semicolons from being treated
	// as statement boundaries.
	var cleaned strings.Builder
	for _, line := range strings.Split(schema, "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		cleaned.WriteString(line)
		cleaned.WriteByte('\n')
	}

	for _, raw := range strings.Split(cleaned.String(), ";") {
		stmt := strings.TrimSpace(raw)
		if stmt == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			errStr := err.Error()
			// Ignore "already exists" and "duplicate key" errors — these occur on
			// re-deploys when the schema was already applied in a previous run.
			if strings.Contains(errStr, "already exists") ||
				strings.Contains(errStr, "duplicate key") {
				continue
			}
			return fmt.Errorf("schema statement failed: %w\nStatement: %.200s", err, stmt)
		}
	}
	return nil
}

func NewAppStoreFromEnv() *AppStore {
	dsn := getenvTrimmed("DATABASE_URL")
	if dsn == "" {
		log.Printf("DATABASE_URL is not set; running with in-memory cache only")
		return newSeededInMemoryStore()
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Printf("Failed to open PostgreSQL connection, continuing in-memory only: %v", err)
		return newSeededInMemoryStore()
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Printf("Failed to ping PostgreSQL, continuing in-memory only: %v", err)
		_ = db.Close()
		return newSeededInMemoryStore()
	}

	if err := applySchema(ctx, db, dbSchemaSQL); err != nil {
		log.Printf("Failed to apply schema, continuing in-memory only: %v", err)
		_ = db.Close()
		return newSeededInMemoryStore()
	}

	store := &AppStore{
		db:          db,
		planTiers:   map[int64]PlanTier{},
		accounts:    map[string]Account{},
		groups:      map[int64]Group{},
		users:       map[int64]User{},
		memberships: map[string]GroupMembership{},
		nlpAcronyms: map[string]NLPAcronymExpansion{},
		nlpPhrases:  map[string]NLPPhraseRewrite{},
	}
	if err := store.warmMemoryFromDB(); err != nil {
		log.Printf("Initial DB warm-up failed (continuing): %v", err)
	}
	if err := store.seedSampleData(context.Background()); err != nil {
		log.Printf("Sample data seed failed (continuing): %v", err)
	}

	return store
}

func (s *AppStore) warmMemoryFromDB() error {
	if s.db == nil {
		return nil
	}

	if _, err := s.ListPlanTiers(context.Background()); err != nil {
		return err
	}
	if _, err := s.ListAccounts(context.Background()); err != nil {
		return err
	}
	if _, err := s.ListGroups(context.Background(), ""); err != nil {
		return err
	}
	if _, err := s.ListUsers(context.Background(), ""); err != nil {
		return err
	}

	return nil
}

func (s *AppStore) DBEnabled() bool {
	return s != nil && s.db != nil
}

func (s *AppStore) UpsertPlanTier(ctx context.Context, in PlanTier) (PlanTier, error) {
	if stringsTrimmed(in.TierCode) == "" {
		return PlanTier{}, fmt.Errorf("tier_code is required")
	}
	if stringsTrimmed(in.DisplayName) == "" {
		in.DisplayName = in.TierCode
	}

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		row := s.db.QueryRowContext(qctx, `
			INSERT INTO plan_tiers (
				tier_code, display_name, monthly_price_usd, storage_bytes,
				monthly_summarize_limit, monthly_analyze_limit, monthly_act_limit
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (tier_code)
			DO UPDATE SET
				display_name=EXCLUDED.display_name,
				monthly_price_usd=EXCLUDED.monthly_price_usd,
				storage_bytes=EXCLUDED.storage_bytes,
				monthly_summarize_limit=EXCLUDED.monthly_summarize_limit,
				monthly_analyze_limit=EXCLUDED.monthly_analyze_limit,
				monthly_act_limit=EXCLUDED.monthly_act_limit
			RETURNING id, tier_code, display_name, monthly_price_usd, storage_bytes,
			          monthly_summarize_limit, monthly_analyze_limit, monthly_act_limit, created_at
		`, in.TierCode, in.DisplayName, in.MonthlyPriceUSD, in.StorageBytes, in.MonthlySummarizeLimit, in.MonthlyAnalyzeLimit, in.MonthlyActLimit)
		if err := row.Scan(&in.ID, &in.TierCode, &in.DisplayName, &in.MonthlyPriceUSD, &in.StorageBytes, &in.MonthlySummarizeLimit, &in.MonthlyAnalyzeLimit, &in.MonthlyActLimit, &in.CreatedAt); err != nil {
			return PlanTier{}, err
		}
	}

	s.mu.Lock()
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	if in.ID == 0 {
		in.ID = int64(len(s.planTiers) + 1)
	}
	s.planTiers[in.ID] = in
	s.mu.Unlock()

	return in, nil
}

func (s *AppStore) ListPlanTiers(ctx context.Context) ([]PlanTier, error) {
	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		rows, err := s.db.QueryContext(qctx, `
			SELECT id, tier_code, display_name, monthly_price_usd, storage_bytes,
			       monthly_summarize_limit, monthly_analyze_limit, monthly_act_limit, created_at
			FROM plan_tiers
			ORDER BY id ASC
		`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		items := []PlanTier{}
		mem := map[int64]PlanTier{}
		for rows.Next() {
			var t PlanTier
			if err := rows.Scan(&t.ID, &t.TierCode, &t.DisplayName, &t.MonthlyPriceUSD, &t.StorageBytes, &t.MonthlySummarizeLimit, &t.MonthlyAnalyzeLimit, &t.MonthlyActLimit, &t.CreatedAt); err != nil {
				return nil, err
			}
			items = append(items, t)
			mem[t.ID] = t
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}

		s.mu.Lock()
		s.planTiers = mem
		s.mu.Unlock()
		return items, nil
	}

	s.mu.RLock()
	items := make([]PlanTier, 0, len(s.planTiers))
	for _, t := range s.planTiers {
		items = append(items, t)
	}
	s.mu.RUnlock()
	return items, nil
}

func (s *AppStore) UpsertAccount(ctx context.Context, in Account) (Account, error) {
	if stringsTrimmed(in.AccountID) == "" {
		return Account{}, fmt.Errorf("account_id is required")
	}

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		row := s.db.QueryRowContext(qctx, `
			INSERT INTO accounts (account_id, display_name, subscribed_plan_tier_id, total_storage_used)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (account_id)
			DO UPDATE SET
				display_name = COALESCE(NULLIF(EXCLUDED.display_name, ''), accounts.display_name),
				subscribed_plan_tier_id=COALESCE(EXCLUDED.subscribed_plan_tier_id, accounts.subscribed_plan_tier_id),
				total_storage_used=COALESCE(EXCLUDED.total_storage_used, accounts.total_storage_used)
			RETURNING account_id, COALESCE(display_name, ''), created_at, COALESCE(subscribed_plan_tier_id,0), total_storage_used
		`, in.AccountID, nullString(in.DisplayName), nullInt64(in.SubscribedPlanTierID), in.TotalStorageUsed)
		if err := row.Scan(&in.AccountID, &in.DisplayName, &in.CreatedAt, &in.SubscribedPlanTierID, &in.TotalStorageUsed); err != nil {
			return Account{}, err
		}
	}

	s.mu.Lock()
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	s.accounts[in.AccountID] = in
	s.mu.Unlock()

	return in, nil
}

func (s *AppStore) GetAccount(ctx context.Context, accountID string) (Account, error) {
	accountID = stringsTrimmed(accountID)
	if accountID == "" {
		return Account{}, fmt.Errorf("account_id is required")
	}

	s.mu.RLock()
	if acc, ok := s.accounts[accountID]; ok {
		s.mu.RUnlock()
		return acc, nil
	}
	s.mu.RUnlock()

	if s.db == nil {
		return Account{}, sql.ErrNoRows
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var acc Account
	err := s.db.QueryRowContext(qctx, `
		SELECT account_id, COALESCE(display_name,''), created_at, COALESCE(subscribed_plan_tier_id,0), total_storage_used
		FROM accounts
		WHERE account_id=$1
	`, accountID).Scan(&acc.AccountID, &acc.DisplayName, &acc.CreatedAt, &acc.SubscribedPlanTierID, &acc.TotalStorageUsed)
	if err != nil {
		return Account{}, err
	}

	s.mu.Lock()
	s.accounts[acc.AccountID] = acc
	s.mu.Unlock()

	return acc, nil
}

func (s *AppStore) ListAccounts(ctx context.Context) ([]Account, error) {
	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		rows, err := s.db.QueryContext(qctx, `
			SELECT account_id, COALESCE(display_name,''), created_at, COALESCE(subscribed_plan_tier_id,0), total_storage_used
			FROM accounts
			ORDER BY created_at DESC
		`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		items := []Account{}
		mem := map[string]Account{}
		for rows.Next() {
			var acc Account
			if err := rows.Scan(&acc.AccountID, &acc.CreatedAt, &acc.SubscribedPlanTierID, &acc.TotalStorageUsed); err != nil {
				return nil, err
			}
			items = append(items, acc)
			mem[acc.AccountID] = acc
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}

		s.mu.Lock()
		s.accounts = mem
		s.mu.Unlock()
		return items, nil
	}

	s.mu.RLock()
	items := make([]Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		items = append(items, a)
	}
	s.mu.RUnlock()
	return items, nil
}

func (s *AppStore) UpsertGroup(ctx context.Context, in Group) (Group, error) {
	if stringsTrimmed(in.AccountID) == "" || stringsTrimmed(in.GroupName) == "" || stringsTrimmed(in.MessengerType) == "" {
		return Group{}, fmt.Errorf("account_id, group_name and messenger_type are required")
	}

	// Generate group_id if not already provided
	if stringsTrimmed(in.GroupID) == "" {
		var err error
		in.GroupID, err = generateGroupID()
		if err != nil {
			return Group{}, fmt.Errorf("failed to generate group_id: %w", err)
		}
	}

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		row := s.db.QueryRowContext(qctx, `
			INSERT INTO account_groups (group_id, account_id, group_name, messenger_type, added_at)
			VALUES ($1,$2,$3,$4,COALESCE($5,NOW()))
			ON CONFLICT (account_id, group_name, messenger_type)
			DO UPDATE SET added_at=COALESCE(EXCLUDED.added_at, account_groups.added_at)
			RETURNING id, group_id, account_id, group_name, messenger_type, added_at
		`, in.GroupID, in.AccountID, in.GroupName, in.MessengerType, nullableTime(in.AddedAt))
		if err := row.Scan(&in.ID, &in.GroupID, &in.AccountID, &in.GroupName, &in.MessengerType, &in.AddedAt); err != nil {
			return Group{}, err
		}
	}

	s.mu.Lock()
	if in.AddedAt.IsZero() {
		in.AddedAt = time.Now().UTC()
	}
	if in.ID == 0 {
		in.ID = int64(len(s.groups) + 1)
	}
	s.groups[in.ID] = in
	s.mu.Unlock()

	return in, nil
}

func (s *AppStore) ListGroups(ctx context.Context, accountID string) ([]Group, error) {
	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()

		query := `
			SELECT id, group_id, account_id, group_name, messenger_type, added_at
			FROM account_groups
		`
		args := []interface{}{}
		if stringsTrimmed(accountID) != "" {
			query += ` WHERE account_id=$1`
			args = append(args, accountID)
		}
		query += ` ORDER BY added_at DESC`

		rows, err := s.db.QueryContext(qctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		items := []Group{}
		mem := map[int64]Group{}
		for rows.Next() {
			var g Group
			if err := rows.Scan(&g.ID, &g.GroupID, &g.AccountID, &g.GroupName, &g.MessengerType, &g.AddedAt); err != nil {
				return nil, err
			}
			items = append(items, g)
			mem[g.ID] = g
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}

		s.mu.Lock()
		for id, g := range mem {
			s.groups[id] = g
		}
		s.mu.Unlock()
		return items, nil
	}

	s.mu.RLock()
	items := []Group{}
	for _, g := range s.groups {
		if accountID == "" || g.AccountID == accountID {
			items = append(items, g)
		}
	}
	s.mu.RUnlock()
	return items, nil
}

// GetUserGroups returns all groups that a user is a member of
func (s *AppStore) GetUserGroups(ctx context.Context, accountID, userName string) ([]Group, error) {
	if stringsTrimmed(accountID) == "" || stringsTrimmed(userName) == "" {
		return nil, fmt.Errorf("account_id and user_name are required")
	}

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()

		query := `
			SELECT DISTINCT ag.id, ag.group_id, ag.account_id, ag.group_name, ag.messenger_type, ag.added_at
			FROM account_groups ag
			INNER JOIN group_memberships gm ON ag.id = gm.group_id
			INNER JOIN account_users au ON gm.user_id = au.id
			WHERE ag.account_id = $1 AND au.user_name = $2 AND au.account_id = $1
			ORDER BY ag.added_at DESC
		`

		rows, err := s.db.QueryContext(qctx, query, accountID, userName)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		items := []Group{}
		for rows.Next() {
			var g Group
			if err := rows.Scan(&g.ID, &g.GroupID, &g.AccountID, &g.GroupName, &g.MessengerType, &g.AddedAt); err != nil {
				return nil, err
			}
			items = append(items, g)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}

		return items, nil
	}

	// In-memory fallback
	return nil, fmt.Errorf("database not configured")
}

// DeleteGroup removes an account_group and all its memberships by group name.
func (s *AppStore) DeleteGroup(ctx context.Context, accountID, groupName string) error {
	accountID = stringsTrimmed(accountID)
	groupName = stringsTrimmed(groupName)
	if accountID == "" || groupName == "" {
		return fmt.Errorf("account_id and group_name are required")
	}

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		// Memberships are deleted via ON DELETE CASCADE on account_groups.
		_, err := s.db.ExecContext(qctx, `
			DELETE FROM account_groups
			WHERE account_id=$1 AND group_name=$2
		`, accountID, groupName)
		if err != nil {
			return err
		}
	}

	// Remove from in-memory cache.
	s.mu.Lock()
	defer s.mu.Unlock()
	var removeID int64
	for id, g := range s.groups {
		if g.AccountID == accountID && g.GroupName == groupName {
			removeID = id
			break
		}
	}
	if removeID != 0 {
		delete(s.groups, removeID)
		for k, m := range s.memberships {
			if m.GroupID == removeID {
				delete(s.memberships, k)
			}
		}
	}
	return nil
}

// RemoveGroupMembership removes a single user from a group.
func (s *AppStore) RemoveGroupMembership(ctx context.Context, accountID, groupName, userName string) error {
	accountID = stringsTrimmed(accountID)
	groupName = stringsTrimmed(groupName)
	userName = stringsTrimmed(userName)
	if accountID == "" || groupName == "" || userName == "" {
		return fmt.Errorf("account_id, group_name and user_name are required")
	}

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		_, err := s.db.ExecContext(qctx, `
			DELETE FROM group_memberships
			WHERE group_id = (
				SELECT id FROM account_groups
				WHERE account_id=$1 AND group_name=$2
				LIMIT 1
			)
			AND user_id = (
				SELECT id FROM account_users
				WHERE account_id=$1 AND user_name=$3
				LIMIT 1
			)
		`, accountID, groupName, userName)
		return err
	}

	// In-memory fallback.
	s.mu.Lock()
	defer s.mu.Unlock()
	var groupID, userID int64
	for _, g := range s.groups {
		if g.AccountID == accountID && g.GroupName == groupName {
			groupID = g.ID
			break
		}
	}
	for _, u := range s.users {
		if u.AccountID == accountID && u.UserName == userName {
			userID = u.ID
			break
		}
	}
	if groupID != 0 && userID != 0 {
		delete(s.memberships, membershipKey(groupID, userID))
	}
	return nil
}

// FindUserInAccount looks up a user by account + userName and returns their DB row.
func (s *AppStore) FindUserInAccount(ctx context.Context, accountID, userName, messengerType string) (User, error) {
	accountID = stringsTrimmed(accountID)
	userName = stringsTrimmed(userName)
	messengerType = stringsTrimmed(messengerType)
	if accountID == "" || userName == "" {
		return User{}, fmt.Errorf("account_id and user_name are required")
	}

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		query := `
			SELECT id, account_id, user_name, messenger_type, COALESCE(messenger_chat_id,''), created_at
			FROM account_users
			WHERE account_id=$1 AND LOWER(user_name)=LOWER($2)
		`
		args := []interface{}{accountID, userName}
		if messengerType != "" {
			query += ` AND messenger_type=$3`
			args = append(args, messengerType)
		}
		query += ` LIMIT 1`
		var u User
		err := s.db.QueryRowContext(qctx, query, args...).Scan(
			&u.ID, &u.AccountID, &u.UserName, &u.MessengerType, &u.MessengerChatID, &u.CreatedAt,
		)
		if err != nil {
			return User{}, err
		}
		return u, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.AccountID == accountID && strings.EqualFold(u.UserName, userName) {
			if messengerType == "" || strings.EqualFold(u.MessengerType, messengerType) {
				return u, nil
			}
		}
	}
	return User{}, fmt.Errorf("user not found")
}

// FindGroupInAccount returns the Group row matching accountID+groupName.
func (s *AppStore) FindGroupInAccount(ctx context.Context, accountID, groupName string) (Group, error) {
	accountID = stringsTrimmed(accountID)
	groupName = stringsTrimmed(groupName)
	if accountID == "" || groupName == "" {
		return Group{}, fmt.Errorf("account_id and group_name are required")
	}

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		var g Group
		err := s.db.QueryRowContext(qctx, `
			SELECT id, group_id, account_id, group_name, messenger_type, added_at
			FROM account_groups
			WHERE account_id=$1 AND LOWER(group_name)=LOWER($2)
			LIMIT 1
		`, accountID, groupName).Scan(&g.ID, &g.GroupID, &g.AccountID, &g.GroupName, &g.MessengerType, &g.AddedAt)
		if err != nil {
			return Group{}, err
		}
		return g, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.groups {
		if g.AccountID == accountID && strings.EqualFold(g.GroupName, groupName) {
			return g, nil
		}
	}
	return Group{}, fmt.Errorf("group not found")
}

func (s *AppStore) LookupUserGroupContext(ctx context.Context, userName, messengerType string) (string, []Group, error) {
	if stringsTrimmed(userName) == "" || stringsTrimmed(messengerType) == "" {
		return "", nil, fmt.Errorf("user_name and messenger_type are required")
	}

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()

		rows, err := s.db.QueryContext(qctx, `
			SELECT ag.id, ag.group_id, ag.account_id, ag.group_name, ag.messenger_type, ag.added_at
			FROM account_groups ag
			INNER JOIN group_memberships gm ON ag.id = gm.group_id
			INNER JOIN account_users au ON gm.user_id = au.id
			WHERE LOWER(au.user_name) = LOWER($1)
			  AND au.messenger_type = $2
			  AND ag.account_id = au.account_id
			ORDER BY ag.account_id, ag.added_at DESC
		`, userName, messengerType)
		if err != nil {
			return "", nil, err
		}
		defer rows.Close()

		groups := []Group{}
		selectedAccountID := ""
		for rows.Next() {
			var g Group
			if err := rows.Scan(&g.ID, &g.GroupID, &g.AccountID, &g.GroupName, &g.MessengerType, &g.AddedAt); err != nil {
				return "", nil, err
			}
			if selectedAccountID == "" {
				selectedAccountID = g.AccountID
			}
			if g.AccountID != selectedAccountID {
				continue
			}
			groups = append(groups, g)
		}
		if err := rows.Err(); err != nil {
			return "", nil, err
		}

		// Fallback: if the user exists but was registered without a messenger_type
		// (placeholder 'unknown' or empty), still return their groups so that the
		// first-message handler can backfill the correct type.
		if len(groups) == 0 {
			fallbackRows, fErr := s.db.QueryContext(qctx, `
				SELECT ag.id, ag.group_id, ag.account_id, ag.group_name, ag.messenger_type, ag.added_at
				FROM account_groups ag
				INNER JOIN group_memberships gm ON ag.id = gm.group_id
				INNER JOIN account_users au ON gm.user_id = au.id
				WHERE LOWER(au.user_name) = LOWER($1)
				  AND au.messenger_type IN ('', 'unknown')
				  AND ag.account_id = au.account_id
				ORDER BY ag.account_id, ag.added_at DESC
			`, userName)
			if fErr == nil {
				defer fallbackRows.Close()
				for fallbackRows.Next() {
					var g Group
					if scanErr := fallbackRows.Scan(&g.ID, &g.GroupID, &g.AccountID, &g.GroupName, &g.MessengerType, &g.AddedAt); scanErr != nil {
						continue
					}
					if selectedAccountID == "" {
						selectedAccountID = g.AccountID
					}
					if g.AccountID != selectedAccountID {
						continue
					}
					groups = append(groups, g)
				}
			}
		}

		return selectedAccountID, groups, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	selectedUserID := int64(0)
	selectedAccountID := ""
	for _, user := range s.users {
		if strings.EqualFold(user.UserName, userName) && strings.EqualFold(user.MessengerType, messengerType) {
			selectedUserID = user.ID
			selectedAccountID = user.AccountID
			break
		}
	}
	// In-memory fallback: also check users with placeholder messenger_type.
	if selectedUserID == 0 {
		for _, user := range s.users {
			if strings.EqualFold(user.UserName, userName) &&
				(user.MessengerType == "" || user.MessengerType == "unknown") {
				selectedUserID = user.ID
				selectedAccountID = user.AccountID
				break
			}
		}
	}
	if selectedUserID == 0 {
		return "", nil, nil
	}

	groups := []Group{}
	for _, membership := range s.memberships {
		if membership.UserID != selectedUserID {
			continue
		}
		group, ok := s.groups[membership.GroupID]
		if !ok || group.AccountID != selectedAccountID {
			continue
		}
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].AddedAt.After(groups[j].AddedAt)
	})
	return selectedAccountID, groups, nil
}

func (s *AppStore) UpsertUser(ctx context.Context, in User) (User, error) {
	if stringsTrimmed(in.AccountID) == "" || stringsTrimmed(in.UserName) == "" || stringsTrimmed(in.MessengerType) == "" {
		return User{}, fmt.Errorf("account_id, user_name and messenger_type are required")
	}

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		row := s.db.QueryRowContext(qctx, `
			INSERT INTO account_users (account_id, user_name, messenger_type, messenger_chat_id, created_at)
			VALUES ($1,$2,$3,$4,COALESCE($5,NOW()))
			ON CONFLICT (account_id, user_name, messenger_type)
			DO UPDATE SET
				messenger_chat_id=COALESCE(EXCLUDED.messenger_chat_id, account_users.messenger_chat_id),
				created_at=COALESCE(EXCLUDED.created_at, account_users.created_at)
			RETURNING id, account_id, user_name, messenger_type, COALESCE(messenger_chat_id,''), created_at
		`, in.AccountID, in.UserName, in.MessengerType, nullString(in.MessengerChatID), nullableTime(in.CreatedAt))
		if err := row.Scan(&in.ID, &in.AccountID, &in.UserName, &in.MessengerType, &in.MessengerChatID, &in.CreatedAt); err != nil {
			return User{}, err
		}
	}

	s.mu.Lock()
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	if in.ID == 0 {
		in.ID = int64(len(s.users) + 1)
	}
	s.users[in.ID] = in
	s.mu.Unlock()

	return in, nil
}

func (s *AppStore) ListUsers(ctx context.Context, accountID string) ([]User, error) {
	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()

		query := `
			SELECT id, account_id, user_name, messenger_type, COALESCE(messenger_chat_id,''), created_at
			FROM account_users
		`
		args := []interface{}{}
		if stringsTrimmed(accountID) != "" {
			query += ` WHERE account_id=$1`
			args = append(args, accountID)
		}
		query += ` ORDER BY created_at DESC`

		rows, err := s.db.QueryContext(qctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		items := []User{}
		mem := map[int64]User{}
		for rows.Next() {
			var u User
			if err := rows.Scan(&u.ID, &u.AccountID, &u.UserName, &u.MessengerType, &u.MessengerChatID, &u.CreatedAt); err != nil {
				return nil, err
			}
			items = append(items, u)
			mem[u.ID] = u
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}

		s.mu.Lock()
		for id, u := range mem {
			s.users[id] = u
		}
		s.mu.Unlock()
		return items, nil
	}

	s.mu.RLock()
	items := []User{}
	for _, u := range s.users {
		if accountID == "" || u.AccountID == accountID {
			items = append(items, u)
		}
	}
	s.mu.RUnlock()
	return items, nil
}

func (s *AppStore) AddGroupMembership(ctx context.Context, groupID, userID int64, addedAt time.Time) (GroupMembership, error) {
	if groupID == 0 || userID == 0 {
		return GroupMembership{}, fmt.Errorf("group_id and user_id are required")
	}

	m := GroupMembership{GroupID: groupID, UserID: userID, AddedAt: addedAt}
	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		row := s.db.QueryRowContext(qctx, `
			INSERT INTO group_memberships (group_id, user_id, added_at)
			VALUES ($1,$2,COALESCE($3,NOW()))
			ON CONFLICT (group_id, user_id)
			DO UPDATE SET added_at=COALESCE(EXCLUDED.added_at, group_memberships.added_at)
			RETURNING group_id, user_id, added_at
		`, groupID, userID, nullableTime(addedAt))
		if err := row.Scan(&m.GroupID, &m.UserID, &m.AddedAt); err != nil {
			return GroupMembership{}, err
		}
	}

	if m.AddedAt.IsZero() {
		m.AddedAt = time.Now().UTC()
	}
	s.mu.Lock()
	s.memberships[membershipKey(groupID, userID)] = m
	s.mu.Unlock()
	return m, nil
}

// IsGroupMember checks if a user is a member of a specific group (GROUP MEMBERSHIP ACL)
func (s *AppStore) IsGroupMember(ctx context.Context, accountID, groupName, userName string) (bool, error) {
	accountID = stringsTrimmed(accountID)
	groupName = stringsTrimmed(groupName)
	userName = stringsTrimmed(userName)

	if accountID == "" || groupName == "" || userName == "" {
		return false, fmt.Errorf("account_id, group_name and user_name are required")
	}

	// Fast path: check in-memory cache first
	s.mu.RLock()
	var targetGroupID int64
	var targetUserID int64

	for _, g := range s.groups {
		if g.AccountID == accountID && g.GroupName == groupName {
			targetGroupID = g.ID
			break
		}
	}
	for _, u := range s.users {
		if u.AccountID == accountID && u.UserName == userName {
			targetUserID = u.ID
			break
		}
	}

	if targetGroupID != 0 && targetUserID != 0 {
		if _, exists := s.memberships[membershipKey(targetGroupID, targetUserID)]; exists {
			s.mu.RUnlock()
			return true, nil
		}
	}
	s.mu.RUnlock()

	// If DB is available, query for membership
	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		var count int
		err := s.db.QueryRowContext(qctx, `
			SELECT COUNT(*) FROM group_memberships gm
			JOIN account_groups ag ON gm.group_id = ag.id
			JOIN account_users au ON gm.user_id = au.id
			WHERE ag.account_id=$1 AND ag.group_name=$2 AND au.account_id=$1 AND au.user_name=$3
		`, accountID, groupName, userName).Scan(&count)
		if err != nil && err != sql.ErrNoRows {
			return false, err
		}
		return count > 0, nil
	}

	return false, nil
}

func (s *AppStore) RecordPlanChangeRequest(ctx context.Context, in PlanChangeRequest) (PlanChangeRequest, error) {
	if stringsTrimmed(in.AccountID) == "" || stringsTrimmed(in.RequestedByUserName) == "" || in.RequestedPlanTierID == 0 {
		return PlanChangeRequest{}, fmt.Errorf("account_id, requested_by_user_name and requested_plan_tier_id are required")
	}

	acc, err := s.GetAccount(ctx, in.AccountID)
	if err != nil {
		return PlanChangeRequest{}, err
	}
	in.CurrentPlanTierID = acc.SubscribedPlanTierID

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		row := s.db.QueryRowContext(qctx, `
			INSERT INTO plan_change_requests (
				account_id, requested_by_user_id, requested_by_user_name, current_plan_tier_id, requested_plan_tier_id
			)
			VALUES ($1,$2,$3,$4,$5)
			RETURNING id, account_id, requested_by_user_id, requested_by_user_name, current_plan_tier_id, requested_plan_tier_id, created_at
		`, in.AccountID, in.RequestedByUserID, in.RequestedByUserName, nullInt64(in.CurrentPlanTierID), in.RequestedPlanTierID)

		var requestedBy sql.NullInt64
		if err := row.Scan(&in.ID, &in.AccountID, &requestedBy, &in.RequestedByUserName, &in.CurrentPlanTierID, &in.RequestedPlanTierID, &in.CreatedAt); err != nil {
			return PlanChangeRequest{}, err
		}
		if requestedBy.Valid {
			v := requestedBy.Int64
			in.RequestedByUserID = &v
		}
	}

	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	return in, nil
}

func (s *AppStore) UpdateAccountPlan(ctx context.Context, accountID string, newPlanTierID int64) (Account, error) {
	if stringsTrimmed(accountID) == "" || newPlanTierID == 0 {
		return Account{}, fmt.Errorf("account_id and new plan tier are required")
	}

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		var updated Account
		err := s.db.QueryRowContext(qctx, `
			UPDATE accounts
			SET subscribed_plan_tier_id=$2
			WHERE account_id=$1
			RETURNING account_id, created_at, COALESCE(subscribed_plan_tier_id,0), total_storage_used
		`, accountID, newPlanTierID).Scan(&updated.AccountID, &updated.CreatedAt, &updated.SubscribedPlanTierID, &updated.TotalStorageUsed)
		if err != nil {
			return Account{}, err
		}

		s.mu.Lock()
		s.accounts[updated.AccountID] = updated
		s.mu.Unlock()
		return updated, nil
	}

	acc, err := s.GetAccount(ctx, accountID)
	if err != nil {
		return Account{}, err
	}
	acc.SubscribedPlanTierID = newPlanTierID
	s.mu.Lock()
	s.accounts[acc.AccountID] = acc
	s.mu.Unlock()
	return acc, nil
}

// GetStorageStatus returns usage percentage and warning message if approaching limit
func (s *AppStore) GetStorageStatus(ctx context.Context, accountID string) (usagePercent float64, warning string, err error) {
	account, err := s.GetAccount(ctx, accountID)
	if err != nil {
		return 0, "", err
	}

	planTier, err := s.GetPlanTierForAccount(ctx, accountID)
	if err != nil || planTier.StorageBytes == 0 {
		return 0, "", nil // No limit
	}

	usagePercent = float64(account.TotalStorageUsed) / float64(planTier.StorageBytes) * 100

	// Warn at 80%, 90%, and 95% usage
	if usagePercent >= 95 {
		warning = "⚠️ CRITICAL: Account storage is 95%+ full. Please delete old messages or upgrade plan."
	} else if usagePercent >= 90 {
		warning = "⚠️ WARNING: Account storage is 90%+ full. Consider deleting old messages soon."
	} else if usagePercent >= 80 {
		warning = "ℹ️ INFO: Account storage is approaching limit (80% full). Plan ahead for cleanup."
	}

	return usagePercent, warning, nil
}

// CheckStorageBeforeSave returns a warning message if the account is running low on storage
func (s *AppStore) CheckStorageBeforeSave(ctx context.Context, accountID string, messageSize int) string {
	_, warning, err := s.GetStorageStatus(ctx, accountID)
	if err != nil {
		return ""
	}

	// Also check if this message would exceed limit
	account, _ := s.GetAccount(ctx, accountID)
	planTier, _ := s.GetPlanTierForAccount(ctx, accountID)

	if planTier.StorageBytes > 0 {
		projectedUsage := account.TotalStorageUsed + int64(messageSize)
		if projectedUsage > planTier.StorageBytes {
			return "❌ STORAGE LIMIT EXCEEDED: Cannot save this message. Please delete old messages or upgrade."
		}
	}

	return warning
}

func (s *AppStore) SaveMessengerInput(ctx context.Context, in MessengerInput) (MessengerInput, error) {
	if stringsTrimmed(in.AccountID) == "" || stringsTrimmed(in.MessengerType) == "" || stringsTrimmed(in.ChatID) == "" || stringsTrimmed(in.MessageType) == "" {
		return MessengerInput{}, fmt.Errorf("account_id, messenger_type, chat_id and message_type are required")
	}
	if len(in.Payload) == 0 {
		in.Payload = json.RawMessage(`{}`)
	}

	encryptedText, err := encryptStringValue(in.TextContent)
	if err != nil {
		return MessengerInput{}, err
	}
	encryptedMediaID, err := encryptStringValue(in.MediaID)
	if err != nil {
		return MessengerInput{}, err
	}
	encryptedMimeType, err := encryptStringValue(in.MimeType)
	if err != nil {
		return MessengerInput{}, err
	}
	encryptedPayload, err := encryptBytesValue([]byte(in.Payload))
	if err != nil {
		return MessengerInput{}, err
	}
	payloadEnvelope, err := json.Marshal(map[string]string{"enc": encryptedPayload})
	if err != nil {
		return MessengerInput{}, err
	}

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		row := s.db.QueryRowContext(qctx, `
			INSERT INTO messenger_inputs (
				account_id, group_name, user_name, messenger_type, chat_id, message_id, message_type,
				text_content, media_id, mime_type, payload, sent_at
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			RETURNING id, received_at
		`, in.AccountID, nullString(in.GroupName), nullString(in.UserName), in.MessengerType, in.ChatID, nullString(in.MessageID), in.MessageType,
			nullString(encryptedText), nullString(encryptedMediaID), nullString(encryptedMimeType), payloadEnvelope, nullableTime(in.SentAt))
		if err := row.Scan(&in.ID, &in.ReceivedAt); err != nil {
			return MessengerInput{}, err
		}
	} else if in.ReceivedAt.IsZero() {
		in.ReceivedAt = time.Now().UTC()
	}

	return in, nil
}

func (s *AppStore) LoadMessengerInputs(ctx context.Context, messengerType, chatID string, start, end time.Time, limit int) ([]MessengerInput, error) {
	if s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = maxSummaryPromptEvents
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(qctx, `
		SELECT id, account_id, COALESCE(group_name,''), COALESCE(user_name,''), messenger_type, chat_id,
		       COALESCE(message_id,''), message_type, COALESCE(text_content,''), COALESCE(media_id,''),
		       COALESCE(mime_type,''), payload, received_at, COALESCE(sent_at, received_at) AS effective_time
		FROM messenger_inputs
		WHERE messenger_type=$1 AND chat_id=$2
		  AND COALESCE(sent_at, received_at) >= $3
		  AND COALESCE(sent_at, received_at) < $4
		ORDER BY COALESCE(sent_at, received_at) ASC
		LIMIT $5
	`, messengerType, chatID, start, end, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []MessengerInput{}
	for rows.Next() {
		var in MessengerInput
		var payload []byte
		if err := rows.Scan(
			&in.ID, &in.AccountID, &in.GroupName, &in.UserName, &in.MessengerType, &in.ChatID,
			&in.MessageID, &in.MessageType, &in.TextContent, &in.MediaID, &in.MimeType, &payload, &in.ReceivedAt, &in.SentAt,
		); err != nil {
			return nil, err
		}
		if stringsTrimmed(in.TextContent) != "" {
			dec, err := decryptStringValue(in.TextContent)
			if err != nil {
				return nil, err
			}
			in.TextContent = dec
		}
		if stringsTrimmed(in.MediaID) != "" {
			dec, err := decryptStringValue(in.MediaID)
			if err != nil {
				return nil, err
			}
			in.MediaID = dec
		}
		if stringsTrimmed(in.MimeType) != "" {
			dec, err := decryptStringValue(in.MimeType)
			if err != nil {
				return nil, err
			}
			in.MimeType = dec
		}

		if len(payload) > 0 {
			var envelope map[string]string
			if err := json.Unmarshal(payload, &envelope); err == nil {
				if enc := stringsTrimmed(envelope["enc"]); enc != "" {
					plain, err := decryptBytesValue(enc)
					if err != nil {
						return nil, err
					}
					in.Payload = json.RawMessage(plain)
				} else {
					in.Payload = json.RawMessage(payload)
				}
			} else {
				in.Payload = json.RawMessage(payload)
			}
		}
		items = append(items, in)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return items, nil
}

func (s *AppStore) SaveSummaryAndDeleteInputs(ctx context.Context, summary MessengerSummary, inputIDs []int64) (MessengerSummary, error) {
	if stringsTrimmed(summary.AccountID) == "" || stringsTrimmed(summary.MessengerType) == "" || stringsTrimmed(summary.ChatID) == "" || stringsTrimmed(summary.SummaryText) == "" {
		return MessengerSummary{}, fmt.Errorf("summary requires account_id, messenger_type, chat_id and summary_text")
	}

	if s.db != nil {
		encryptedSummary, err := encryptStringValue(summary.SummaryText)
		if err != nil {
			return MessengerSummary{}, err
		}
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()

		tx, err := s.db.BeginTx(qctx, nil)
		if err != nil {
			return MessengerSummary{}, err
		}

		row := tx.QueryRowContext(qctx, `
			INSERT INTO messenger_summaries (
				account_id, messenger_type, chat_id, domain, window_start, window_end, source_message_count, summary_text
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			RETURNING id, account_id, messenger_type, chat_id, COALESCE(domain,''), window_start, window_end, source_message_count, summary_text, summarized_at
		`, summary.AccountID, summary.MessengerType, summary.ChatID, nullString(summary.Domain), summary.WindowStart, summary.WindowEnd, summary.SourceMessageCount, encryptedSummary)
		if err := row.Scan(
			&summary.ID, &summary.AccountID, &summary.MessengerType, &summary.ChatID, &summary.Domain,
			&summary.WindowStart, &summary.WindowEnd, &summary.SourceMessageCount, &summary.SummaryText, &summary.SummarizedAt,
		); err != nil {
			_ = tx.Rollback()
			return MessengerSummary{}, err
		}

		if len(inputIDs) > 0 {
			if _, err := tx.ExecContext(qctx, `DELETE FROM messenger_inputs WHERE id = ANY($1)`, pq.Int64Array(inputIDs)); err != nil {
				_ = tx.Rollback()
				return MessengerSummary{}, err
			}
		}

		if err := tx.Commit(); err != nil {
			return MessengerSummary{}, err
		}

		if stringsTrimmed(summary.SummaryText) != "" {
			decryptedSummary, err := decryptStringValue(summary.SummaryText)
			if err != nil {
				return MessengerSummary{}, err
			}
			summary.SummaryText = decryptedSummary
		}
	}

	if summary.SummarizedAt.IsZero() {
		summary.SummarizedAt = time.Now().UTC()
	}
	return summary, nil
}

func (s *AppStore) GetPlanTierForAccount(ctx context.Context, accountID string) (PlanTier, error) {
	acc, err := s.GetAccount(ctx, accountID)
	if err != nil {
		return PlanTier{}, err
	}
	if acc.SubscribedPlanTierID == 0 {
		return PlanTier{}, fmt.Errorf("account has no subscribed plan tier")
	}

	s.mu.RLock()
	if tier, ok := s.planTiers[acc.SubscribedPlanTierID]; ok {
		s.mu.RUnlock()
		return tier, nil
	}
	s.mu.RUnlock()

	if s.db == nil {
		return PlanTier{}, sql.ErrNoRows
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var tier PlanTier
	err = s.db.QueryRowContext(qctx, `
		SELECT id, tier_code, display_name, monthly_price_usd, storage_bytes,
		       monthly_summarize_limit, monthly_analyze_limit, monthly_act_limit, created_at
		FROM plan_tiers
		WHERE id=$1
	`, acc.SubscribedPlanTierID).Scan(
		&tier.ID, &tier.TierCode, &tier.DisplayName, &tier.MonthlyPriceUSD, &tier.StorageBytes,
		&tier.MonthlySummarizeLimit, &tier.MonthlyAnalyzeLimit, &tier.MonthlyActLimit, &tier.CreatedAt,
	)
	if err != nil {
		return PlanTier{}, err
	}

	s.mu.Lock()
	s.planTiers[tier.ID] = tier
	s.mu.Unlock()
	return tier, nil
}

func (s *AppStore) CountCommandUsage(ctx context.Context, accountID, groupName, command string, windowStart, windowEnd time.Time) (int, error) {
	if s.db == nil {
		return 0, nil
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var count int
	err := s.db.QueryRowContext(qctx, `
		SELECT COUNT(1)
		FROM command_usage_events
		WHERE account_id=$1 AND group_name=$2 AND command_name=$3 AND executed_at >= $4 AND executed_at < $5
	`, accountID, groupName, command, windowStart, windowEnd).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (s *AppStore) RecordCommandUsage(ctx context.Context, accountID, groupName, command string, executedAt time.Time) error {
	if s.db == nil {
		return nil
	}
	if stringsTrimmed(accountID) == "" || stringsTrimmed(groupName) == "" || stringsTrimmed(command) == "" {
		return fmt.Errorf("account_id, group_name and command are required")
	}
	if executedAt.IsZero() {
		executedAt = time.Now().UTC()
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.db.ExecContext(qctx, `
		INSERT INTO command_usage_events (account_id, group_name, command_name, executed_at)
		VALUES ($1,$2,$3,$4)
	`, accountID, groupName, command, executedAt)
	return err
}

func (s *AppStore) LastCommandUsageTime(ctx context.Context, accountID, groupName, command string) (time.Time, bool, error) {
	if s.db == nil {
		return time.Time{}, false, nil
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var last sql.NullTime
	err := s.db.QueryRowContext(qctx, `
		SELECT MAX(executed_at)
		FROM command_usage_events
		WHERE account_id=$1 AND group_name=$2 AND command_name=$3
	`, accountID, groupName, command).Scan(&last)
	if err != nil {
		return time.Time{}, false, err
	}
	if !last.Valid {
		return time.Time{}, false, nil
	}
	return last.Time, true, nil
}

func (s *AppStore) LatestSummary(ctx context.Context, messengerType, chatID, domain string) (MessengerSummary, error) {
	if s.db == nil {
		return MessengerSummary{}, sql.ErrNoRows
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	row := s.db.QueryRowContext(qctx, `
		SELECT id, account_id, messenger_type, chat_id, COALESCE(domain,''), window_start, window_end, source_message_count, summary_text, summarized_at
		FROM messenger_summaries
		WHERE messenger_type=$1 AND chat_id=$2 AND ($3='' OR COALESCE(domain,'')=$3)
		ORDER BY summarized_at DESC
		LIMIT 1
	`, messengerType, chatID, stringsTrimmed(domain))

	var item MessengerSummary
	err := row.Scan(
		&item.ID, &item.AccountID, &item.MessengerType, &item.ChatID, &item.Domain,
		&item.WindowStart, &item.WindowEnd, &item.SourceMessageCount, &item.SummaryText, &item.SummarizedAt,
	)
	if err != nil {
		return MessengerSummary{}, err
	}
	if stringsTrimmed(item.SummaryText) != "" {
		decryptedSummary, err := decryptStringValue(item.SummaryText)
		if err != nil {
			return MessengerSummary{}, err
		}
		item.SummaryText = decryptedSummary
	}
	return item, nil
}

// SummaryForRange returns the most recent stored summary whose window overlaps [start, end).
// Returns sql.ErrNoRows when no matching summary exists.
func (s *AppStore) SummaryForRange(ctx context.Context, messengerType, chatID, domain string, start, end time.Time) (MessengerSummary, error) {
	if s.db == nil {
		return MessengerSummary{}, sql.ErrNoRows
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	row := s.db.QueryRowContext(qctx, `
		SELECT id, account_id, messenger_type, chat_id, COALESCE(domain,''), window_start, window_end, source_message_count, summary_text, summarized_at
		FROM messenger_summaries
		WHERE messenger_type=$1 AND chat_id=$2 AND ($3='' OR COALESCE(domain,'')=$3)
		  AND window_end > $4 AND window_start < $5
		ORDER BY summarized_at DESC
		LIMIT 1
	`, messengerType, chatID, stringsTrimmed(domain), start, end)

	var item MessengerSummary
	err := row.Scan(
		&item.ID, &item.AccountID, &item.MessengerType, &item.ChatID, &item.Domain,
		&item.WindowStart, &item.WindowEnd, &item.SourceMessageCount, &item.SummaryText, &item.SummarizedAt,
	)
	if err != nil {
		return MessengerSummary{}, err
	}
	if stringsTrimmed(item.SummaryText) != "" {
		decryptedSummary, err := decryptStringValue(item.SummaryText)
		if err != nil {
			return MessengerSummary{}, err
		}
		item.SummaryText = decryptedSummary
	}
	return item, nil
}

func (s *AppStore) CountNewMessengerInputsSince(ctx context.Context, messengerType, chatID string, since time.Time) (int, error) {
	if s.db == nil {
		return 0, nil
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var count int
	err := s.db.QueryRowContext(qctx, `
		SELECT COUNT(1)
		FROM messenger_inputs
		WHERE messenger_type=$1
		  AND chat_id=$2
		  AND received_at > $3
		  AND (
			message_type <> 'text'
			OR COALESCE(text_content,'') !~* '^\\s*koko\\s*:'
		  )
	`, messengerType, chatID, since).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (s *AppStore) seedSampleData(ctx context.Context) error {
	totalStorageBytes := int64(90 * 1024 * 1024 * 1024)
	if raw := getenvTrimmed("KOKO_TOTAL_STORAGE_BYTES"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			totalStorageBytes = parsed
		}
	}

	gold := PlanTier{
		TierCode:              "gold",
		DisplayName:           "Gold",
		MonthlyPriceUSD:       90,
		StorageBytes:          totalStorageBytes,
		MonthlySummarizeLimit: 20,
		MonthlyAnalyzeLimit:   12,
		MonthlyActLimit:       9,
	}
	silver := PlanTier{
		TierCode:              "silver",
		DisplayName:           "Silver",
		MonthlyPriceUSD:       gold.MonthlyPriceUSD / 2,
		StorageBytes:          int64(math.Round(float64(gold.StorageBytes) / 2)),
		MonthlySummarizeLimit: int(math.Round(float64(gold.MonthlySummarizeLimit) / 2)),
		MonthlyAnalyzeLimit:   int(math.Round(float64(gold.MonthlyAnalyzeLimit) / 2)),
		MonthlyActLimit:       int(math.Round(float64(gold.MonthlyActLimit) / 2)),
	}
	base := PlanTier{
		TierCode:              "base",
		DisplayName:           "Base",
		MonthlyPriceUSD:       gold.MonthlyPriceUSD / 3,
		StorageBytes:          int64(math.Round(float64(gold.StorageBytes) / 3)),
		MonthlySummarizeLimit: int(math.Round(float64(gold.MonthlySummarizeLimit) / 3)),
		MonthlyAnalyzeLimit:   int(math.Round(float64(gold.MonthlyAnalyzeLimit) / 3)),
		MonthlyActLimit:       int(math.Round(float64(gold.MonthlyActLimit) / 3)),
	}

	for _, tier := range []PlanTier{base, silver, gold} {
		if _, err := s.UpsertPlanTier(ctx, tier); err != nil {
			return err
		}
	}

	tiers, err := s.ListPlanTiers(ctx)
	if err != nil {
		return err
	}
	goldTierID := int64(0)
	defaultTierID := int64(0)
	for _, t := range tiers {
		if defaultTierID == 0 {
			defaultTierID = t.ID
		}
		if strings.EqualFold(t.TierCode, "gold") {
			goldTierID = t.ID
		}
	}
	if goldTierID == 0 {
		goldTierID = defaultTierID
	}

	if _, err := s.UpsertAccount(ctx, Account{AccountID: "demo_account_a", SubscribedPlanTierID: goldTierID}); err != nil {
		return err
	}

	groupIDs := map[string]int64{}
	for _, groupName := range []string{"revenue", "industrial", "electronics", "consumer"} {
		g, err := s.UpsertGroup(ctx, Group{AccountID: "demo_account_a", GroupName: groupName, MessengerType: "telegram"})
		if err != nil {
			return err
		}
		groupIDs[groupName] = g.ID
	}

	type seedUser struct {
		Name  string
		Phone string
	}
	for _, su := range []seedUser{
		{"demo_user_1", "+15555550101"},
		{"demo_user_6", ""},
		{"demo_user_2", "+15555550102"},
	} {
		user, err := s.UpsertUser(ctx, User{
			AccountID:       "demo_account_a",
			UserName:        su.Name,
			MessengerType:   "telegram",
			MessengerChatID: su.Phone,
		})
		if err != nil {
			return err
		}

		for _, groupName := range []string{"revenue", "industrial", "electronics", "consumer"} {
			if groupID := groupIDs[groupName]; groupID != 0 {
				if _, err := s.AddGroupMembership(ctx, groupID, user.ID, time.Now().UTC()); err != nil {
					return err
				}
			}
		}
	}

	if _, err := s.UpsertAccount(ctx, Account{AccountID: "demo_account_b", SubscribedPlanTierID: goldTierID}); err != nil {
		return err
	}

	demoBGroupIDs := map[string]int64{}
	for _, groupName := range []string{"revenue", "industrial", "electronics", "consumer"} {
		g, err := s.UpsertGroup(ctx, Group{AccountID: "demo_account_b", GroupName: groupName, MessengerType: "telegram"})
		if err != nil {
			return err
		}
		demoBGroupIDs[groupName] = g.ID
	}

	for _, su := range []seedUser{
		{"demo_user_1", "+15555550101"},
		{"demo_user_3", "+15555550103"},
	} {
		user, err := s.UpsertUser(ctx, User{
			AccountID:       "demo_account_b",
			UserName:        su.Name,
			MessengerType:   "telegram",
			MessengerChatID: su.Phone,
		})
		if err != nil {
			return err
		}

		for _, groupName := range []string{"revenue", "industrial", "electronics", "consumer"} {
			if groupID := demoBGroupIDs[groupName]; groupID != 0 {
				if _, err := s.AddGroupMembership(ctx, groupID, user.ID, time.Now().UTC()); err != nil {
					return err
				}
			}
		}
	}

	if _, err := s.UpsertAccount(ctx, Account{AccountID: "demo_account_c", SubscribedPlanTierID: goldTierID}); err != nil {
		return err
	}

	demoCGroupIDs := map[string]int64{}
	for _, groupName := range []string{"revenue", "industrial", "electronics", "consumer"} {
		g, err := s.UpsertGroup(ctx, Group{AccountID: "demo_account_c", GroupName: groupName, MessengerType: "telegram"})
		if err != nil {
			return err
		}
		demoCGroupIDs[groupName] = g.ID
	}

	for _, su := range []seedUser{
		{"demo_user_4", "+15555550104"},
	} {
		user, err := s.UpsertUser(ctx, User{
			AccountID:       "demo_account_c",
			UserName:        su.Name,
			MessengerType:   "telegram",
			MessengerChatID: su.Phone,
		})
		if err != nil {
			return err
		}

		for _, groupName := range []string{"revenue", "industrial", "electronics", "consumer"} {
			if groupID := demoCGroupIDs[groupName]; groupID != 0 {
				if _, err := s.AddGroupMembership(ctx, groupID, user.ID, time.Now().UTC()); err != nil {
					return err
				}
			}
		}
	}

	if _, err := s.UpsertAccount(ctx, Account{AccountID: "demo_account_d", SubscribedPlanTierID: goldTierID}); err != nil {
		return err
	}

	coolGroupIDs := map[string]int64{}
	for _, groupName := range []string{"revenue", "industrial", "electronics", "consumer"} {
		g, err := s.UpsertGroup(ctx, Group{AccountID: "demo_account_d", GroupName: groupName, MessengerType: "telegram"})
		if err != nil {
			return err
		}
		coolGroupIDs[groupName] = g.ID
	}

	for _, su := range []seedUser{
		{"demo_user_5", "+15555550105"},
	} {
		user, err := s.UpsertUser(ctx, User{
			AccountID:       "demo_account_d",
			UserName:        su.Name,
			MessengerType:   "telegram",
			MessengerChatID: su.Phone,
		})
		if err != nil {
			return err
		}

		for _, groupName := range []string{"revenue", "industrial", "electronics", "consumer"} {
			if groupID := coolGroupIDs[groupName]; groupID != 0 {
				if _, err := s.AddGroupMembership(ctx, groupID, user.ID, time.Now().UTC()); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func (s *AppStore) EnsureAccountGroupUser(ctx context.Context, accountID, groupName, userName, messengerType string) error {
	accountID = stringsTrimmed(accountID)
	groupName = stringsTrimmed(groupName)
	userName = stringsTrimmed(userName)
	messengerType = stringsTrimmed(messengerType)
	if accountID == "" || messengerType == "" {
		return fmt.Errorf("account_id and messenger_type are required")
	}

	acc, err := s.GetAccount(ctx, accountID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		tiers, tierErr := s.ListPlanTiers(ctx)
		if tierErr != nil {
			return tierErr
		}
		defaultTierID := int64(0)
		if len(tiers) > 0 {
			defaultTierID = tiers[0].ID
		}
		if _, upsertErr := s.UpsertAccount(ctx, Account{AccountID: accountID, SubscribedPlanTierID: defaultTierID}); upsertErr != nil {
			return upsertErr
		}
	} else if acc.SubscribedPlanTierID == 0 {
		tiers, tierErr := s.ListPlanTiers(ctx)
		if tierErr != nil {
			return tierErr
		}
		if len(tiers) > 0 {
			if _, upsertErr := s.UpsertAccount(ctx, Account{AccountID: accountID, SubscribedPlanTierID: tiers[0].ID, TotalStorageUsed: acc.TotalStorageUsed}); upsertErr != nil {
				return upsertErr
			}
		}
	}

	if groupName != "" {
		if _, err := s.UpsertGroup(ctx, Group{AccountID: accountID, GroupName: groupName, MessengerType: messengerType}); err != nil {
			return err
		}
	}

	if userName != "" {
		user, err := s.UpsertUser(ctx, User{AccountID: accountID, UserName: userName, MessengerType: messengerType})
		if err != nil {
			return err
		}

		if groupName != "" {
			groups, err := s.ListGroups(ctx, accountID)
			if err != nil {
				return err
			}
			for _, g := range groups {
				if g.GroupName == groupName && g.MessengerType == messengerType {
					if _, err := s.AddGroupMembership(ctx, g.ID, user.ID, time.Now().UTC()); err != nil {
						return err
					}
					break
				}
			}
		}
	}

	return nil
}

func (s *AppStore) RecordPolicyAudit(ctx context.Context, in PolicyAuditLog) (PolicyAuditLog, error) {
	if stringsTrimmed(in.Action) == "" || stringsTrimmed(in.ReasonCode) == "" {
		return PolicyAuditLog{}, fmt.Errorf("action and reason_code are required")
	}
	if len(in.Details) == 0 {
		in.Details = json.RawMessage(`{}`)
	}

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		row := s.db.QueryRowContext(qctx, `
			INSERT INTO policy_audit_logs (
				account_id, group_name, user_name, messenger_type, chat_id, message_id,
				message_type, action, reason_code, warning_message, details
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			RETURNING id, created_at
		`,
			nullString(in.AccountID),
			nullString(in.GroupName),
			nullString(in.UserName),
			nullString(in.MessengerType),
			nullString(in.ChatID),
			nullString(in.MessageID),
			nullString(in.MessageType),
			in.Action,
			in.ReasonCode,
			nullString(in.WarningMessage),
			[]byte(in.Details),
		)
		if err := row.Scan(&in.ID, &in.CreatedAt); err != nil {
			return PolicyAuditLog{}, err
		}
	}

	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	return in, nil
}

// ============ USP Feature Methods ============

// SaveMessageSentiment stores sentiment analysis for a message
func (s *AppStore) SaveMessageSentiment(ctx context.Context, in MessageSentiment) error {
	if s.db == nil {
		return nil
	}

	if in.AnalyzedAt.IsZero() {
		in.AnalyzedAt = time.Now().UTC()
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.db.ExecContext(qctx, `
		INSERT INTO message_sentiment (account_id, group_name, message_id, sentiment_score, sentiment_label, emotion_tags, confidence, analyzed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT DO NOTHING
	`, in.AccountID, in.GroupName, in.MessageID, in.SentimentScore, in.SentimentLabel, in.EmotionTags, in.Confidence, in.AnalyzedAt)
	return err
}

// SaveExtractedTodo stores an extracted todo item
func (s *AppStore) SaveExtractedTodo(ctx context.Context, in ExtractedTodo) (ExtractedTodo, error) {
	if s.db == nil {
		return in, nil
	}

	if in.ExtractedAt.IsZero() {
		in.ExtractedAt = time.Now().UTC()
	}
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = time.Now().UTC()
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	row := s.db.QueryRowContext(qctx, `
		INSERT INTO extracted_todos (account_id, group_name, message_id, extracted_from_user, todo_text, assigned_to, due_date, priority, status, confidence, tags, extracted_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id
	`, in.AccountID, in.GroupName, in.MessageID, in.ExtractedFromUser, in.TodoText, nullString(in.AssignedTo), in.DueDate, in.Priority, in.Status, in.Confidence, in.Tags, in.ExtractedAt, in.UpdatedAt)

	err := row.Scan(&in.ID)
	return in, err
}

// UpdateTodoStatus updates the status of an extracted todo
func (s *AppStore) UpdateTodoStatus(ctx context.Context, todoID int64, newStatus string) error {
	if s.db == nil {
		return nil
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.db.ExecContext(qctx, `
		UPDATE extracted_todos SET status=$1, updated_at=NOW() WHERE id=$2
	`, newStatus, todoID)
	return err
}

// GetActiveTodos returns pending/in-progress todos for a group
func (s *AppStore) GetActiveTodos(ctx context.Context, accountID, groupName string) ([]ExtractedTodo, error) {
	if s.db == nil {
		return nil, nil
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(qctx, `
		SELECT id, account_id, group_name, message_id, extracted_from_user, todo_text, assigned_to, due_date, priority, status, confidence, tags, extracted_at, updated_at
		FROM extracted_todos
		WHERE account_id=$1 AND group_name=$2 AND status IN ('pending', 'in_progress')
		ORDER BY COALESCE(due_date, NOW()) ASC
	`, accountID, groupName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	todos := []ExtractedTodo{}
	for rows.Next() {
		var t ExtractedTodo
		var dueDate sql.NullTime
		var assignedTo sql.NullString
		err := rows.Scan(&t.ID, &t.AccountID, &t.GroupName, &t.MessageID, &t.ExtractedFromUser, &t.TodoText, &assignedTo, &dueDate, &t.Priority, &t.Status, &t.Confidence, &t.Tags, &t.ExtractedAt, &t.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if assignedTo.Valid {
			t.AssignedTo = assignedTo.String
		}
		if dueDate.Valid {
			t.DueDate = &dueDate.Time
		}
		todos = append(todos, t)
	}
	return todos, rows.Err()
}

// SaveKnowledgeBaseArticle stores a KB article
func (s *AppStore) SaveKnowledgeBaseArticle(ctx context.Context, in KnowledgeBaseArticle) (KnowledgeBaseArticle, error) {
	if s.db == nil {
		return in, nil
	}

	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = time.Now().UTC()
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	row := s.db.QueryRowContext(qctx, `
		INSERT INTO knowledge_base_articles (account_id, group_name, title, content, auto_generated, source_messages, category, tags, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id
	`, in.AccountID, nullString(in.GroupName), in.Title, in.Content, in.AutoGenerated, in.SourceMessages, nullString(in.Category), in.Tags, in.CreatedAt, in.UpdatedAt)

	err := row.Scan(&in.ID)
	return in, err
}

// SaveGroupAnalytics stores calculated analytics for a group
func (s *AppStore) SaveGroupAnalytics(ctx context.Context, in GroupAnalytics) error {
	if s.db == nil {
		return nil
	}

	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.db.ExecContext(qctx, `
		INSERT INTO group_analytics (account_id, group_name, analysis_date, message_count, unique_participants, avg_sentiment_score, dominant_sentiment, top_topics, todo_count, todo_completed, engagement_score, health_score, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT DO NOTHING
	`, in.AccountID, in.GroupName, in.AnalysisDate, in.MessageCount, in.UniqueParticipants, in.AvgSentimentScore, in.DominantSentiment, in.TopTopics, in.TodoCount, in.TodoCompleted, in.EngagementScore, in.HealthScore, in.CreatedAt)
	return err
}

// GetGroupAnalytics retrieves analytics for a group over time
func (s *AppStore) GetGroupAnalytics(ctx context.Context, accountID, groupName string, days int) ([]GroupAnalytics, error) {
	if s.db == nil {
		return nil, nil
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(qctx, `
		SELECT id, account_id, group_name, analysis_date, message_count, unique_participants, avg_sentiment_score, dominant_sentiment, top_topics, todo_count, todo_completed, engagement_score, health_score, created_at
		FROM group_analytics
		WHERE account_id=$1 AND group_name=$2 AND analysis_date >= CURRENT_DATE - INTERVAL '1 day' * $3
		ORDER BY analysis_date DESC
	`, accountID, groupName, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	analytics := []GroupAnalytics{}
	for rows.Next() {
		var a GroupAnalytics
		err := rows.Scan(&a.ID, &a.AccountID, &a.GroupName, &a.AnalysisDate, &a.MessageCount, &a.UniqueParticipants, &a.AvgSentimentScore, &a.DominantSentiment, &a.TopTopics, &a.TodoCount, &a.TodoCompleted, &a.EngagementScore, &a.HealthScore, &a.CreatedAt)
		if err != nil {
			return nil, err
		}
		analytics = append(analytics, a)
	}
	return analytics, rows.Err()
}

// UpsertUserPreferences saves or updates user preferences
func (s *AppStore) UpsertUserPreferences(ctx context.Context, in UserPreferences) (UserPreferences, error) {
	if s.db == nil {
		return in, nil
	}

	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	in.UpdatedAt = time.Now().UTC()

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	row := s.db.QueryRowContext(qctx, `
		INSERT INTO user_preferences (account_id, user_name, preferred_language, email_digest_frequency, enable_sentiment_analysis, enable_topic_extraction, enable_todo_extraction, enable_voice_summaries, dark_mode, timezone, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (account_id, user_name) DO UPDATE SET
			preferred_language=EXCLUDED.preferred_language,
			email_digest_frequency=EXCLUDED.email_digest_frequency,
			enable_sentiment_analysis=EXCLUDED.enable_sentiment_analysis,
			enable_topic_extraction=EXCLUDED.enable_topic_extraction,
			enable_todo_extraction=EXCLUDED.enable_todo_extraction,
			enable_voice_summaries=EXCLUDED.enable_voice_summaries,
			dark_mode=EXCLUDED.dark_mode,
			timezone=EXCLUDED.timezone,
			updated_at=NOW()
		RETURNING id, created_at, updated_at
	`, in.AccountID, nullString(in.UserName), in.PreferredLanguage, in.EmailDigestFrequency, in.EnableSentimentAnalysis, in.EnableTopicExtraction, in.EnableTodoExtraction, in.EnableVoiceSummaries, in.DarkMode, in.Timezone, in.CreatedAt, in.UpdatedAt)

	err := row.Scan(&in.ID, &in.CreatedAt, &in.UpdatedAt)
	return in, err
}

// SaveConversationTimeline records significant events in group conversation
func (s *AppStore) SaveConversationTimeline(ctx context.Context, in ConversationTimeline) error {
	if s.db == nil {
		return nil
	}

	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.db.ExecContext(qctx, `
		INSERT INTO conversation_timeline (account_id, group_name, event_time, event_type, event_description, source_message_ids, sentiment_context, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, in.AccountID, in.GroupName, in.EventTime, in.EventType, in.EventDescription, in.SourceMessageIDs, nullString(in.SentimentContext), in.CreatedAt)
	return err
}

func membershipKey(groupID, userID int64) string {
	return fmt.Sprintf("%d:%d", groupID, userID)
}

// ============ BROADCAST: Update user messenger_chat_id ============

// UpsertUserChatID stores or updates the Telegram chat_id for a user so we can broadcast to them later.
func (s *AppStore) UpsertUserChatID(ctx context.Context, accountID, userName, messengerType, chatID string) error {
	if s.db == nil {
		return nil
	}
	if stringsTrimmed(accountID) == "" || stringsTrimmed(userName) == "" || stringsTrimmed(chatID) == "" {
		return nil
	}
	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.db.ExecContext(qctx, `
		UPDATE account_users
		SET messenger_chat_id = $4
		WHERE account_id=$1 AND user_name=$2 AND messenger_type=$3
	`, accountID, userName, messengerType, chatID)
	return err
}

// BackfillUserMessengerType sets messenger_type and messenger_chat_id for users
// that were registered without a messenger type (i.e. messenger_type = 'unknown'
// or the placeholder used when adding a user via koko:addUser before their first
// real message).  It only updates rows where messenger_type is the placeholder
// value and the user_name matches — it never overwrites an already-set type.
func (s *AppStore) BackfillUserMessengerType(ctx context.Context, accountID, userName, messengerType, chatID string) error {
	if s.db == nil {
		// In-memory mode: update the users map in place.
		s.mu.Lock()
		defer s.mu.Unlock()
		for id, u := range s.users {
			if u.AccountID == accountID && strings.EqualFold(u.UserName, userName) &&
				(u.MessengerType == "" || u.MessengerType == "unknown") {
				u.MessengerType = messengerType
				u.MessengerChatID = chatID
				s.users[id] = u
			}
		}
		return nil
	}

	if stringsTrimmed(accountID) == "" || stringsTrimmed(userName) == "" || stringsTrimmed(messengerType) == "" {
		return nil
	}

	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.db.ExecContext(qctx, `
		UPDATE account_users
		SET messenger_type    = $3,
		    messenger_chat_id = COALESCE(NULLIF($4,''), messenger_chat_id)
		WHERE account_id = $1
		  AND LOWER(user_name) = LOWER($2)
		  AND messenger_type IN ('', 'unknown')
	`, accountID, userName, messengerType, chatID)
	return err
}

// HasUserTOUSent returns true when the Terms of Use notice has already been
// sent to the given user (account_users.tou_sent_at IS NOT NULL). Falls back
// to false when DB is unavailable so the notice is still sent in-process.
func (s *AppStore) HasUserTOUSent(ctx context.Context, accountID, userName string) (bool, error) {
	if s.db == nil {
		return false, nil
	}
	if stringsTrimmed(accountID) == "" || stringsTrimmed(userName) == "" {
		return false, nil
	}
	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var sent bool
	err := s.db.QueryRowContext(qctx, `
		SELECT (tou_sent_at IS NOT NULL)
		FROM account_users
		WHERE account_id=$1 AND LOWER(user_name)=LOWER($2)
		LIMIT 1
	`, accountID, userName).Scan(&sent)
	if err != nil {
		// User row not found yet or column missing — treat as not sent.
		return false, nil
	}
	return sent, nil
}

// MarkUserTOUSent sets tou_sent_at=NOW() for the given user so subsequent
// interactions skip the ToU notice.
func (s *AppStore) MarkUserTOUSent(ctx context.Context, accountID, userName string) error {
	if s.db == nil {
		return nil
	}
	if stringsTrimmed(accountID) == "" || stringsTrimmed(userName) == "" {
		return nil
	}
	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.db.ExecContext(qctx, `
		UPDATE account_users
		SET tou_sent_at = NOW()
		WHERE account_id=$1 AND LOWER(user_name)=LOWER($2) AND tou_sent_at IS NULL
	`, accountID, userName)
	return err
}

// GetGroupMemberChatIDs returns all non-empty messenger_chat_ids for users in the given group.
func (s *AppStore) GetGroupMemberChatIDs(ctx context.Context, accountID, groupName string) ([]int64, error) {
	if s.db == nil {
		return nil, nil
	}
	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(qctx, `
		SELECT CAST(au.messenger_chat_id AS BIGINT)
		FROM account_users au
		INNER JOIN group_memberships gm ON au.id = gm.user_id
		INNER JOIN account_groups ag ON gm.group_id = ag.id
		WHERE ag.account_id = $1
		  AND ag.group_name = $2
		  AND au.messenger_chat_id IS NOT NULL
		  AND au.messenger_chat_id <> ''
		  AND au.messenger_chat_id ~ '^-?[0-9]+$'
	`, accountID, groupName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chatIDs []int64
	for rows.Next() {
		var cid int64
		if err := rows.Scan(&cid); err != nil {
			continue
		}
		chatIDs = append(chatIDs, cid)
	}
	return chatIDs, rows.Err()
}

// GetGroupMemberDestinations returns all non-empty (messenger_type, chat_id)
// destinations for users in the given account group.
func (s *AppStore) GetGroupMemberDestinations(ctx context.Context, accountID, groupName string) ([]GroupMemberDestination, error) {
	if s.db == nil {
		return nil, nil
	}
	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()

	rows, err := s.db.QueryContext(qctx, `
		SELECT au.messenger_type, COALESCE(au.messenger_chat_id, '')
		FROM account_users au
		INNER JOIN group_memberships gm ON au.id = gm.user_id
		INNER JOIN account_groups ag ON gm.group_id = ag.id
		WHERE ag.account_id = $1
		  AND ag.group_name = $2
		  AND au.messenger_chat_id IS NOT NULL
		  AND au.messenger_chat_id <> ''
	`, accountID, groupName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []GroupMemberDestination{}
	seen := map[string]bool{}
	for rows.Next() {
		var d GroupMemberDestination
		if err := rows.Scan(&d.MessengerType, &d.ChatID); err != nil {
			return nil, err
		}
		d.MessengerType = strings.TrimSpace(strings.ToLower(d.MessengerType))
		d.ChatID = strings.TrimSpace(d.ChatID)
		if d.MessengerType == "" || d.ChatID == "" {
			continue
		}
		key := d.MessengerType + "::" + d.ChatID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GetGroupUsers returns all user names in the given account group.
func (s *AppStore) GetGroupUsers(ctx context.Context, accountID, groupName string) ([]string, error) {
	if s.db == nil {
		return nil, nil
	}
	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()

	rows, err := s.db.QueryContext(qctx, `
		SELECT u.user_name
		FROM account_users u
		JOIN group_memberships m ON u.id = m.user_id
		JOIN account_groups g ON m.group_id = g.id
		WHERE g.account_id = $1 AND g.group_name = $2
	`, accountID, groupName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ============ KOKO COMMAND AUDIT ============

// KokoCommandAudit records a koko:summarize / koko:analyze / koko:act request and its confirmation outcome.
type KokoCommandAudit struct {
	ID                   int64      `json:"id"`
	AccountID            string     `json:"account_id"`
	GroupID              string     `json:"group_id"`
	GroupName            string     `json:"group_name"`
	CommandName          string     `json:"command_name"`
	RequestedBy          string     `json:"requested_by"`
	RequestedAt          time.Time  `json:"requested_at"`
	ConfirmationResponse *string    `json:"confirmation_response,omitempty"`
	ConfirmationAt       *time.Time `json:"confirmation_at,omitempty"`
}

// RecordKokoCommandAudit inserts a new audit record and returns it with the assigned ID.
func (s *AppStore) RecordKokoCommandAudit(ctx context.Context, in KokoCommandAudit) (KokoCommandAudit, error) {
	if s.db == nil {
		in.ID = 0
		if in.RequestedAt.IsZero() {
			in.RequestedAt = time.Now().UTC()
		}
		return in, nil
	}
	if in.RequestedAt.IsZero() {
		in.RequestedAt = time.Now().UTC()
	}
	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	err := s.db.QueryRowContext(qctx, `
		INSERT INTO koko_command_audit
			(account_id, group_id, group_name, command_name, requested_by, requested_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING id, requested_at
	`, in.AccountID, in.GroupID, in.GroupName, in.CommandName, in.RequestedBy, in.RequestedAt).Scan(&in.ID, &in.RequestedAt)
	return in, err
}

// UpdateKokoCommandAuditConfirmation sets confirmation_response and confirmation_at on an existing audit row.
func (s *AppStore) UpdateKokoCommandAuditConfirmation(ctx context.Context, auditID int64, response string) error {
	if s.db == nil || auditID == 0 {
		return nil
	}
	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.db.ExecContext(qctx, `
		UPDATE koko_command_audit
		SET confirmation_response=$2, confirmation_at=NOW()
		WHERE id=$1
	`, auditID, response)
	return err
}

// ============ UNSUMMARIZED INPUTS ============

// LoadInputsSinceLastSummary returns messenger_inputs received after the most recent summary's window_end.
// When no summary exists it returns all stored inputs. Also returns the cutoff time used.
func (s *AppStore) LoadInputsSinceLastSummary(ctx context.Context, messengerType, chatID string, limit int) ([]MessengerInput, time.Time, error) {
	if s.db == nil {
		return nil, time.Time{}, nil
	}
	if limit <= 0 {
		limit = maxSummaryPromptEvents
	}

	// Find last summary window_end for this chat
	var since time.Time
	var lastWindowEnd sql.NullTime
	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_ = s.db.QueryRowContext(qctx, `
		SELECT MAX(window_end)
		FROM messenger_summaries
		WHERE messenger_type=$1 AND chat_id=$2
	`, messengerType, chatID).Scan(&lastWindowEnd)
	if lastWindowEnd.Valid {
		since = lastWindowEnd.Time
	}

	// Load inputs after that cutoff, filtering out koko: commands.
	// Order and filter by COALESCE(sent_at, received_at) so multi-timezone messages
	// appear in their actual chronological send order rather than server ingest order.
	qctx2, cancel2 := context.WithTimeout(ctx, dbTimeout)
	defer cancel2()
	rows, err := s.db.QueryContext(qctx2, `
		SELECT id, account_id, COALESCE(group_name,''), COALESCE(user_name,''), messenger_type, chat_id,
		       COALESCE(message_id,''), message_type, COALESCE(text_content,''), COALESCE(media_id,''),
		       COALESCE(mime_type,''), payload, received_at, COALESCE(sent_at, received_at) AS effective_time
		FROM messenger_inputs
		WHERE messenger_type=$1 AND chat_id=$2
		  AND ($3::timestamptz IS NULL OR COALESCE(sent_at, received_at) > $3)
		  AND (message_type <> 'text' OR COALESCE(text_content,'') !~* '^\s*koko\s*:')
		ORDER BY COALESCE(sent_at, received_at) ASC
		LIMIT $4
	`, messengerType, chatID, nullableTime(since), limit)
	if err != nil {
		return nil, since, err
	}
	defer rows.Close()

	items := []MessengerInput{}
	for rows.Next() {
		var in MessengerInput
		var payload []byte
		if err := rows.Scan(
			&in.ID, &in.AccountID, &in.GroupName, &in.UserName, &in.MessengerType, &in.ChatID,
			&in.MessageID, &in.MessageType, &in.TextContent, &in.MediaID, &in.MimeType, &payload, &in.ReceivedAt, &in.SentAt,
		); err != nil {
			return nil, since, err
		}
		if stringsTrimmed(in.TextContent) != "" {
			dec, _ := decryptStringValue(in.TextContent)
			in.TextContent = dec
		}
		if len(payload) > 0 {
			var envelope map[string]string
			if json.Unmarshal(payload, &envelope) == nil {
				if enc := stringsTrimmed(envelope["enc"]); enc != "" {
					if plain, decErr := decryptBytesValue(enc); decErr == nil {
						in.Payload = json.RawMessage(plain)
					} else {
						in.Payload = json.RawMessage(payload)
					}
				} else {
					in.Payload = json.RawMessage(payload)
				}
			} else {
				in.Payload = json.RawMessage(payload)
			}
		}
		items = append(items, in)
	}
	if err := rows.Err(); err != nil {
		return nil, since, err
	}
	return items, since, nil
}

func nullString(v string) interface{} {
	v = stringsTrimmed(v)
	if v == "" {
		return nil
	}
	return v
}

func nullInt64(v int64) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

func nullableTime(v time.Time) interface{} {
	if v.IsZero() {
		return nil
	}
	return v
}

func nlpNormScopeValue(value, fallback string) string {
	v := strings.ToLower(stringsTrimmed(value))
	if v == "" {
		return fallback
	}
	return v
}

func nlpScopeKey(parts ...string) string {
	return strings.Join(parts, "::")
}

func (s *AppStore) UpsertNLPAcronymExpansion(ctx context.Context, in NLPAcronymExpansion) (NLPAcronymExpansion, error) {
	in.AccountID = stringsTrimmed(in.AccountID)
	in.GroupName = stringsTrimmed(in.GroupName)
	in.Domain = nlpNormScopeValue(in.Domain, featureModeAll)
	in.Locale = nlpNormScopeValue(in.Locale, "en")
	in.ShortForm = strings.ToLower(stringsTrimmed(in.ShortForm))
	in.ExpandedForm = stringsTrimmed(in.ExpandedForm)
	in.Source = stringsTrimmed(in.Source)
	if in.Source == "" {
		in.Source = "user"
	}
	if in.AccountID == "" || in.ShortForm == "" || in.ExpandedForm == "" {
		return NLPAcronymExpansion{}, fmt.Errorf("account_id, short_form and expanded_form are required")
	}

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		row := s.db.QueryRowContext(qctx, `
			INSERT INTO nlp_acronym_expansions (
				account_id, group_name, domain, locale, short_form, expanded_form, source, learned_at, updated_at
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,COALESCE($8,NOW()),NOW())
			ON CONFLICT (account_id, group_name, domain, locale, short_form)
			DO UPDATE SET
				expanded_form=EXCLUDED.expanded_form,
				source=EXCLUDED.source,
				updated_at=NOW()
			RETURNING id, account_id, group_name, domain, locale, short_form, expanded_form, source, learned_at, updated_at
		`, in.AccountID, in.GroupName, in.Domain, in.Locale, in.ShortForm, in.ExpandedForm, in.Source, nullableTime(in.LearnedAt))
		if err := row.Scan(&in.ID, &in.AccountID, &in.GroupName, &in.Domain, &in.Locale, &in.ShortForm, &in.ExpandedForm, &in.Source, &in.LearnedAt, &in.UpdatedAt); err != nil {
			return NLPAcronymExpansion{}, err
		}
	}

	if in.LearnedAt.IsZero() {
		in.LearnedAt = time.Now().UTC()
	}
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = time.Now().UTC()
	}

	s.mu.Lock()
	if in.ID == 0 {
		in.ID = int64(len(s.nlpAcronyms) + 1)
	}
	s.nlpAcronyms[nlpScopeKey(in.AccountID, in.GroupName, in.Domain, in.Locale, in.ShortForm)] = in
	s.mu.Unlock()

	return in, nil
}

func (s *AppStore) ListNLPAcronymExpansions(ctx context.Context, accountID, groupName, domain, locale string) ([]NLPAcronymExpansion, error) {
	accountID = stringsTrimmed(accountID)
	if accountID == "" {
		return nil, fmt.Errorf("account_id is required")
	}
	groupName = stringsTrimmed(groupName)
	domain = nlpNormScopeValue(domain, featureModeAll)
	locale = nlpNormScopeValue(locale, "en")

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		rows, err := s.db.QueryContext(qctx, `
			SELECT id, account_id, group_name, domain, locale, short_form, expanded_form, source, learned_at, updated_at
			FROM nlp_acronym_expansions
			WHERE account_id=$1
			  AND group_name IN ('', $2)
			  AND domain IN ('all', $3)
			  AND locale IN ('en', $4)
			ORDER BY updated_at DESC
		`, accountID, groupName, domain, locale)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		out := make([]NLPAcronymExpansion, 0)
		for rows.Next() {
			var item NLPAcronymExpansion
			if err := rows.Scan(&item.ID, &item.AccountID, &item.GroupName, &item.Domain, &item.Locale, &item.ShortForm, &item.ExpandedForm, &item.Source, &item.LearnedAt, &item.UpdatedAt); err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return out, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]NLPAcronymExpansion, 0)
	for _, item := range s.nlpAcronyms {
		if item.AccountID != accountID {
			continue
		}
		if item.GroupName != "" && item.GroupName != groupName {
			continue
		}
		if item.Domain != featureModeAll && item.Domain != domain {
			continue
		}
		if item.Locale != "en" && item.Locale != locale {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *AppStore) UpsertNLPPhraseRewrite(ctx context.Context, in NLPPhraseRewrite) (NLPPhraseRewrite, error) {
	in.AccountID = stringsTrimmed(in.AccountID)
	in.GroupName = stringsTrimmed(in.GroupName)
	in.Domain = nlpNormScopeValue(in.Domain, featureModeAll)
	in.Locale = nlpNormScopeValue(in.Locale, "en")
	in.SourcePhrase = strings.ToLower(stringsTrimmed(in.SourcePhrase))
	in.NormalizedPhrase = stringsTrimmed(in.NormalizedPhrase)
	in.Source = stringsTrimmed(in.Source)
	if in.Source == "" {
		in.Source = "user"
	}
	if in.AccountID == "" || in.SourcePhrase == "" || in.NormalizedPhrase == "" {
		return NLPPhraseRewrite{}, fmt.Errorf("account_id, source_phrase and normalized_phrase are required")
	}

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		row := s.db.QueryRowContext(qctx, `
			INSERT INTO nlp_phrase_rewrites (
				account_id, group_name, domain, locale, source_phrase, normalized_phrase, source, learned_at, updated_at
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,COALESCE($8,NOW()),NOW())
			ON CONFLICT (account_id, group_name, domain, locale, source_phrase)
			DO UPDATE SET
				normalized_phrase=EXCLUDED.normalized_phrase,
				source=EXCLUDED.source,
				updated_at=NOW()
			RETURNING id, account_id, group_name, domain, locale, source_phrase, normalized_phrase, source, learned_at, updated_at
		`, in.AccountID, in.GroupName, in.Domain, in.Locale, in.SourcePhrase, in.NormalizedPhrase, in.Source, nullableTime(in.LearnedAt))
		if err := row.Scan(&in.ID, &in.AccountID, &in.GroupName, &in.Domain, &in.Locale, &in.SourcePhrase, &in.NormalizedPhrase, &in.Source, &in.LearnedAt, &in.UpdatedAt); err != nil {
			return NLPPhraseRewrite{}, err
		}
	}

	if in.LearnedAt.IsZero() {
		in.LearnedAt = time.Now().UTC()
	}
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = time.Now().UTC()
	}

	s.mu.Lock()
	if in.ID == 0 {
		in.ID = int64(len(s.nlpPhrases) + 1)
	}
	s.nlpPhrases[nlpScopeKey(in.AccountID, in.GroupName, in.Domain, in.Locale, in.SourcePhrase)] = in
	s.mu.Unlock()

	return in, nil
}

func (s *AppStore) ListNLPPhraseRewrites(ctx context.Context, accountID, groupName, domain, locale string) ([]NLPPhraseRewrite, error) {
	accountID = stringsTrimmed(accountID)
	if accountID == "" {
		return nil, fmt.Errorf("account_id is required")
	}
	groupName = stringsTrimmed(groupName)
	domain = nlpNormScopeValue(domain, featureModeAll)
	locale = nlpNormScopeValue(locale, "en")

	if s.db != nil {
		qctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		rows, err := s.db.QueryContext(qctx, `
			SELECT id, account_id, group_name, domain, locale, source_phrase, normalized_phrase, source, learned_at, updated_at
			FROM nlp_phrase_rewrites
			WHERE account_id=$1
			  AND group_name IN ('', $2)
			  AND domain IN ('all', $3)
			  AND locale IN ('en', $4)
			ORDER BY updated_at DESC
		`, accountID, groupName, domain, locale)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		out := make([]NLPPhraseRewrite, 0)
		for rows.Next() {
			var item NLPPhraseRewrite
			if err := rows.Scan(&item.ID, &item.AccountID, &item.GroupName, &item.Domain, &item.Locale, &item.SourcePhrase, &item.NormalizedPhrase, &item.Source, &item.LearnedAt, &item.UpdatedAt); err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return out, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]NLPPhraseRewrite, 0)
	for _, item := range s.nlpPhrases {
		if item.AccountID != accountID {
			continue
		}
		if item.GroupName != "" && item.GroupName != groupName {
			continue
		}
		if item.Domain != featureModeAll && item.Domain != domain {
			continue
		}
		if item.Locale != "en" && item.Locale != locale {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func stringsTrimmed(v string) string {
	return strings.TrimSpace(v)
}

func getenvTrimmed(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

// generateGroupID generates a unique group ID using random bytes
func generateGroupID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "grp_" + hex.EncodeToString(b), nil
}

// ============================================================
// API Key auth (for REST + GraphQL endpoints)
// ============================================================

type KokoAPIKey struct {
	ID        int64     `json:"id"`
	AccountID string    `json:"account_id"`
	UserName  string    `json:"user_name"`
	APIKey    string    `json:"api_key"`
	CreatedAt time.Time `json:"created_at"`
}

// ValidateAPIKey looks up a raw Bearer token and returns the associated KokoAPIKey.
func (s *AppStore) ValidateAPIKey(ctx context.Context, rawKey string) (KokoAPIKey, error) {
	var out KokoAPIKey
	if s.db == nil {
		return out, errors.New("db unavailable")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, account_id, user_name, api_key, created_at
		FROM koko_api_keys WHERE api_key = $1 LIMIT 1`, rawKey)
	if err := row.Scan(&out.ID, &out.AccountID, &out.UserName, &out.APIKey, &out.CreatedAt); err != nil {
		return out, err
	}
	return out, nil
}

// ============================================================
// Act Prompts (koko:act AI output stored per account/user/group)
// ============================================================

type KokoActPrompt struct {
	ID         int64     `json:"id"`
	AccountID  string    `json:"account_id"`
	UserName   string    `json:"user_name"`
	GroupID    string    `json:"group_id"`
	GroupName  string    `json:"group_name"`
	Domain     string    `json:"domain"`
	PromptText string    `json:"prompt_text"`
	CreatedAt  time.Time `json:"created_at"`
}

// SaveActPrompt persists an AI prompt generated by koko:act.
func (s *AppStore) SaveActPrompt(ctx context.Context, in KokoActPrompt) (KokoActPrompt, error) {
	if s.db == nil {
		in.ID = 0
		if in.CreatedAt.IsZero() {
			in.CreatedAt = time.Now().UTC()
		}
		return in, nil
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO koko_act_prompts
			(account_id, user_name, group_id, group_name, domain, prompt_text, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING id, account_id, user_name, group_id, group_name, domain, prompt_text, created_at`,
		in.AccountID, in.UserName, in.GroupID, in.GroupName, in.Domain, in.PromptText, in.CreatedAt)
	if err := row.Scan(&in.ID, &in.AccountID, &in.UserName, &in.GroupID, &in.GroupName,
		&in.Domain, &in.PromptText, &in.CreatedAt); err != nil {
		return in, err
	}
	return in, nil
}

// ListActPrompts returns act prompts for an account, with optional group, domain, and date range filters.
// Returns (results, totalCount, error). Page and pageSize are 1-based and max page size is capped at 50.
func (s *AppStore) ListActPrompts(ctx context.Context, accountID, groupName, domain string, from, to time.Time, page, pageSize int) ([]KokoActPrompt, int, error) {
	if s.db == nil {
		return nil, 0, errors.New("db unavailable")
	}
	if pageSize <= 0 || pageSize > 50 {
		pageSize = 50
	}
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * pageSize

	args := []interface{}{accountID}
	conds := []string{"account_id = $1"}
	n := 2
	if strings.TrimSpace(groupName) != "" {
		conds = append(conds, fmt.Sprintf("group_name = $%d", n))
		args = append(args, groupName)
		n++
	}
	if strings.TrimSpace(domain) != "" {
		conds = append(conds, fmt.Sprintf("domain = $%d", n))
		args = append(args, domain)
		n++
	}
	if !from.IsZero() {
		conds = append(conds, fmt.Sprintf("created_at >= $%d", n))
		args = append(args, from)
		n++
	}
	if !to.IsZero() {
		conds = append(conds, fmt.Sprintf("created_at <= $%d", n))
		args = append(args, to)
		n++
	}
	where := "WHERE " + strings.Join(conds, " AND ")

	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM koko_act_prompts "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, pageSize, offset)
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, account_id, user_name, group_id, group_name, domain, prompt_text, created_at FROM koko_act_prompts "+
			where+" ORDER BY created_at DESC LIMIT $"+fmt.Sprint(n)+" OFFSET $"+fmt.Sprint(n+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []KokoActPrompt
	for rows.Next() {
		var r KokoActPrompt
		if err := rows.Scan(&r.ID, &r.AccountID, &r.UserName, &r.GroupID, &r.GroupName, &r.Domain, &r.PromptText, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// ListSummaries returns messenger summaries for an account with optional filters.
// Returns (results, totalCount, error). Page and pageSize are 1-based; max pageSize capped at 50.
func (s *AppStore) ListSummaries(ctx context.Context, accountID, groupName, domain string, from, to time.Time, page, pageSize int) ([]MessengerSummary, int, error) {
	if s.db == nil {
		return nil, 0, errors.New("db unavailable")
	}
	if pageSize <= 0 || pageSize > 50 {
		pageSize = 50
	}
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * pageSize

	args := []interface{}{accountID}
	conds := []string{"account_id = $1"}
	n := 2
	// groupName maps to chat_id via a join; without that we filter on account_id only
	// For simplicity, filter by domain directly
	if strings.TrimSpace(domain) != "" {
		conds = append(conds, fmt.Sprintf("domain = $%d", n))
		args = append(args, domain)
		n++
	}
	if !from.IsZero() {
		conds = append(conds, fmt.Sprintf("summarized_at >= $%d", n))
		args = append(args, from)
		n++
	}
	if !to.IsZero() {
		conds = append(conds, fmt.Sprintf("summarized_at <= $%d", n))
		args = append(args, to)
		n++
	}
	where := "WHERE " + strings.Join(conds, " AND ")

	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messenger_summaries "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, pageSize, offset)
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, account_id, messenger_type, chat_id, COALESCE(domain,''), window_start, window_end, source_message_count, summary_text, summarized_at FROM messenger_summaries "+
			where+" ORDER BY summarized_at DESC LIMIT $"+fmt.Sprint(n)+" OFFSET $"+fmt.Sprint(n+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []MessengerSummary
	for rows.Next() {
		var r MessengerSummary
		if err := rows.Scan(&r.ID, &r.AccountID, &r.MessengerType, &r.ChatID, &r.Domain,
			&r.WindowStart, &r.WindowEnd, &r.SourceMessageCount, &r.SummaryText, &r.SummarizedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}
