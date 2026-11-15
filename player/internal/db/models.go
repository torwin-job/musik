package db

// Row types for the identity, account, token and job tables. Columns map by
// `db` tag through sqlx; nullable columns are pointers.

type UserRecord struct {
	ID          string `db:"id"`
	Status      string `db:"status"`
	DisplayName string `db:"display_name"`
	CreatedAt   string `db:"created_at"`
	UpdatedAt   string `db:"updated_at"`
}

type ProfileRecord struct {
	ID          string  `db:"id"`
	OwnerUserID string  `db:"owner_user_id"`
	Name        string  `db:"name"`
	IsDefault   int     `db:"is_default"`
	CreatedAt   string  `db:"created_at"`
	UpdatedAt   string  `db:"updated_at"`
	DeletedAt   *string `db:"deleted_at"`
}

const profileColumns = `id, owner_user_id, name, is_default, created_at, updated_at, deleted_at`

type AuthSessionRecord struct {
	TokenHash       string  `db:"token_hash"`
	UserID          string  `db:"user_id"`
	ActiveProfileID string  `db:"active_profile_id"`
	CSRFHash        string  `db:"csrf_hash"`
	CreatedAt       string  `db:"created_at"`
	ExpiresAt       string  `db:"expires_at"`
	RevokedAt       *string `db:"revoked_at"`
}

type DeviceTokenRecord struct {
	ID         string  `db:"id"`
	SecretHash string  `db:"secret_hash"`
	UserID     string  `db:"user_id"`
	ProfileID  string  `db:"profile_id"`
	Name       string  `db:"name"`
	CreatedAt  string  `db:"created_at"`
	LastUsedAt *string `db:"last_used_at"`
	ExpiresAt  string  `db:"expires_at"`
	RevokedAt  *string `db:"revoked_at"`
}

type InvitationRecord struct {
	ID           string  `db:"id"`
	SecretHash   string  `db:"secret_hash"`
	CreatedBy    *string `db:"created_by"`
	Issuer       *string `db:"issuer"`
	Email        *string `db:"email"`
	CreatedAt    string  `db:"created_at"`
	ExpiresAt    string  `db:"expires_at"`
	ConsumedAt   *string `db:"consumed_at"`
	RevokedAt    *string `db:"revoked_at"`
	ConsumedBy   *string `db:"consumed_by"`
	TargetUserID *string `db:"target_user_id"`
}

const invitationColumns = `id, secret_hash, created_by, issuer, email, created_at, expires_at,
consumed_at, revoked_at, consumed_by, target_user_id`

type JobRecord struct {
	ID          int64   `db:"id"`
	Kind        string  `db:"kind"`
	Status      string  `db:"status"`
	PayloadJSON *string `db:"payload_json"`
	ResultJSON  *string `db:"result_json"`
	Error       *string `db:"error"`
	CreatedAt   string  `db:"created_at"`
	UpdatedAt   string  `db:"updated_at"`
}

const jobColumns = `id, kind, status, payload_json, result_json, error, created_at, updated_at`

type PluginJobRecord struct {
	ID              int64   `db:"id"`
	PluginID        string  `db:"plugin_id"`
	Action          string  `db:"action"`
	InputJSON       string  `db:"input_json"`
	Status          string  `db:"status"`
	ProgressJSON    *string `db:"progress_json"`
	ResultJSON      *string `db:"result_json"`
	Error           *string `db:"error"`
	ErrorCode       *string `db:"error_code"`
	DownstreamJobID *int64  `db:"downstream_job_id"`
	CreatedAt       string  `db:"created_at"`
	UpdatedAt       string  `db:"updated_at"`
}

const pluginJobColumns = `id, plugin_id, action, input_json, status, progress_json, result_json, error, error_code, downstream_job_id, created_at, updated_at`
