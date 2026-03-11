package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) ActivateInvite(ctx context.Context, in ActivateInviteInput) (DeviceActivationStatus, error) {
	code := strings.ToUpper(strings.TrimSpace(in.Code))
	if code == "" || strings.TrimSpace(in.DeviceFingerprint) == "" {
		return DeviceActivationStatus{}, fmt.Errorf("%w: invite code and device fingerprint are required", ErrBadRequest)
	}

	deviceHash := hashString(strings.TrimSpace(in.DeviceFingerprint))
	now := time.Now().UTC()

	var existingCode string
	var existingActivatedAt, existingExpiresAt time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT i.code, ia.activated_at, ia.expires_at
         FROM invite_activations ia
         JOIN invites i ON i.id = ia.invite_id
         WHERE ia.device_hash = $1 AND ia.expires_at > NOW()`,
		deviceHash,
	).Scan(&existingCode, &existingActivatedAt, &existingExpiresAt)
	if err == nil {
		return DeviceActivationStatus{
			Allowed:     true,
			InviteCode:  existingCode,
			ActivatedAt: existingActivatedAt,
			ExpiresAt:   existingExpiresAt,
		}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return DeviceActivationStatus{}, err
	}

	var inviteID uuid.UUID
	err = s.pool.QueryRow(ctx,
		`SELECT id
         FROM invites
         WHERE code = $1
           AND is_revoked = FALSE
           AND expires_at > NOW()
           AND used_count < max_uses`,
		code,
	).Scan(&inviteID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DeviceActivationStatus{}, fmt.Errorf("%w: invite not found or inactive", ErrBadRequest)
		}
		return DeviceActivationStatus{}, err
	}

	activationExpiresAt := now.Add(time.Duration(s.cfg.Runtime.Invite.DeviceActivationLifetimeHours) * time.Hour)

	_, err = s.pool.Exec(ctx,
		`INSERT INTO invite_activations (id, invite_id, device_hash, activated_at, expires_at)
         VALUES ($1, $2, $3, $4, $5)
         ON CONFLICT (device_hash)
         DO UPDATE SET invite_id = EXCLUDED.invite_id, activated_at = EXCLUDED.activated_at, expires_at = EXCLUDED.expires_at, used_by_user_id = NULL`,
		uuid.New(), inviteID, deviceHash, now, activationExpiresAt,
	)
	if err != nil {
		return DeviceActivationStatus{}, err
	}

	return DeviceActivationStatus{
		Allowed:     true,
		InviteCode:  code,
		ActivatedAt: now,
		ExpiresAt:   activationExpiresAt,
	}, nil
}

func (s *Service) CheckDeviceActivation(ctx context.Context, deviceFingerprint string) (DeviceActivationStatus, error) {
	if strings.TrimSpace(deviceFingerprint) == "" {
		return DeviceActivationStatus{Allowed: false}, nil
	}

	deviceHash := hashString(strings.TrimSpace(deviceFingerprint))

	var code string
	var activatedAt, expiresAt time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT i.code, ia.activated_at, ia.expires_at
         FROM invite_activations ia
         JOIN invites i ON i.id = ia.invite_id
         WHERE ia.device_hash = $1
           AND ia.expires_at > NOW()
           AND ia.used_by_user_id IS NULL`,
		deviceHash,
	).Scan(&code, &activatedAt, &expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DeviceActivationStatus{Allowed: false}, nil
		}
		return DeviceActivationStatus{}, err
	}

	return DeviceActivationStatus{
		Allowed:     true,
		InviteCode:  code,
		ActivatedAt: activatedAt,
		ExpiresAt:   expiresAt,
	}, nil
}

func (s *Service) SendVerificationCode(ctx context.Context, in SendCodeInput) (SendCodeResult, error) {
	email := normalizeEmail(in.Email)
	if email == "" {
		return SendCodeResult{}, fmt.Errorf("%w: email is required", ErrBadRequest)
	}

	purpose := strings.ToLower(strings.TrimSpace(in.Purpose))
	if purpose != "register" && purpose != "login" {
		return SendCodeResult{}, fmt.Errorf("%w: invalid purpose", ErrBadRequest)
	}

	if err := s.verifyCaptcha(ctx, in.CaptchaToken); err != nil {
		return SendCodeResult{}, err
	}

	deviceHash := hashString(strings.TrimSpace(in.DeviceFingerprint))
	if purpose == "register" {
		var activationID uuid.UUID
		err := s.pool.QueryRow(ctx,
			`SELECT id
             FROM invite_activations
             WHERE device_hash = $1
               AND used_by_user_id IS NULL
               AND expires_at > NOW()`,
			deviceHash,
		).Scan(&activationID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return SendCodeResult{}, fmt.Errorf("%w: device is not activated by invite", ErrForbidden)
			}
			return SendCodeResult{}, err
		}
	}

	if purpose == "login" {
		var userID uuid.UUID
		err := s.pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&userID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return SendCodeResult{}, fmt.Errorf("%w: account not found", ErrNotFound)
			}
			return SendCodeResult{}, err
		}
	}

	code := randomDigits6()
	codeHash := hashCode(code, s.cfg.Security.VerificationPepper)
	expiresAt := time.Now().UTC().Add(15 * time.Minute)

	_, err := s.pool.Exec(ctx,
		`INSERT INTO verification_codes (id, email, purpose, code_hash, device_hash, expires_at, ip_address)
         VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		uuid.New(), email, purpose, codeHash, deviceHash, expiresAt, in.IPAddress,
	)
	if err != nil {
		return SendCodeResult{}, err
	}

	if sendErr := s.sendVerificationEmail(email, code, purpose); sendErr != nil {
		log.Printf("send verification email failed: %v", sendErr)
	}

	result := SendCodeResult{
		ExpiresAt: expiresAt,
		Delivery:  "email",
	}
	if s.cfg.Runtime.ExposeDebugVerificationCode || s.cfg.Runtime.Debug {
		result.DebugCode = code
	}

	return result, nil
}

func (s *Service) Register(ctx context.Context, in RegisterInput, ipAddress, userAgent string) (AuthResult, error) {
	email := normalizeEmail(in.Email)
	if email == "" {
		return AuthResult{}, fmt.Errorf("%w: email is required", ErrBadRequest)
	}

	nickname := normalizeNickname(in.Nickname)
	if nickname == "" {
		return AuthResult{}, fmt.Errorf("%w: nickname is required", ErrBadRequest)
	}

	if strings.TrimSpace(in.FirstName) == "" || strings.TrimSpace(in.LastName) == "" {
		return AuthResult{}, fmt.Errorf("%w: first_name and last_name are required", ErrBadRequest)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AuthResult{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	deviceHash := hashString(strings.TrimSpace(in.DeviceFingerprint))

	var activationID, inviteID, inviterID uuid.UUID
	err = tx.QueryRow(ctx,
		`SELECT ia.id, ia.invite_id, i.inviter_user_id
         FROM invite_activations ia
         JOIN invites i ON i.id = ia.invite_id
         WHERE ia.device_hash = $1
           AND ia.used_by_user_id IS NULL
           AND ia.expires_at > NOW()
         FOR UPDATE`,
		deviceHash,
	).Scan(&activationID, &inviteID, &inviterID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AuthResult{}, fmt.Errorf("%w: invite activation not found", ErrForbidden)
		}
		return AuthResult{}, err
	}

	if verifyErr := s.consumeVerificationCodeTx(ctx, tx, email, "register", deviceHash, in.VerificationCode); verifyErr != nil {
		return AuthResult{}, verifyErr
	}

	var exists bool
	if scanErr := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE LOWER(nickname::text) = LOWER($1))`,
		nickname,
	).Scan(&exists); scanErr != nil {
		return AuthResult{}, scanErr
	}
	if exists {
		return AuthResult{}, fmt.Errorf("%w: nickname is already in use", ErrBadRequest)
	}

	if scanErr := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE email = $1)`,
		email,
	).Scan(&exists); scanErr != nil {
		return AuthResult{}, scanErr
	}
	if exists {
		return AuthResult{}, fmt.Errorf("%w: account with this email already exists", ErrBadRequest)
	}

	userID := uuid.New()
	userUID := makeUserUID()

	recoveryMetaBytes, _ := json.Marshal(in.RecoveryMeta)

	_, err = tx.Exec(ctx,
		`INSERT INTO users (
            id, uid, email, first_name, last_name, nickname,
            trust_level, invited_by_user_id, recovery_bundle_ciphertext,
            recovery_bundle_meta, metadata
         ) VALUES ($1,$2,$3,$4,$5,$6,1,$7,$8,$9,'{}'::jsonb)`,
		userID,
		userUID,
		email,
		strings.TrimSpace(in.FirstName),
		strings.TrimSpace(in.LastName),
		nickname,
		inviterID,
		strings.TrimSpace(in.RecoveryCiphertext),
		string(recoveryMetaBytes),
	)
	if err != nil {
		return AuthResult{}, err
	}

	_, err = tx.Exec(ctx,
		`UPDATE invite_activations SET used_by_user_id = $2 WHERE id = $1`,
		activationID, userID,
	)
	if err != nil {
		return AuthResult{}, err
	}

	_, err = tx.Exec(ctx,
		`UPDATE invites
         SET used_count = used_count + 1
         WHERE id = $1`,
		inviteID,
	)
	if err != nil {
		return AuthResult{}, err
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO invite_edges (id, inviter_user_id, invitee_user_id, invite_id)
         VALUES ($1, $2, $3, $4)
         ON CONFLICT (invitee_user_id) DO NOTHING`,
		uuid.New(), inviterID, userID, inviteID,
	)
	if err != nil {
		return AuthResult{}, err
	}

	if err = s.ensureDefaultConversationsTx(ctx, tx, userID, inviterID); err != nil {
		return AuthResult{}, err
	}

	sessionID := uuid.New()
	token, exp, issueErr := s.jwt.Issue(userID, sessionID, false)
	if issueErr != nil {
		return AuthResult{}, issueErr
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO user_sessions (id, user_id, device_hash, jwt_id, created_at, ip_address, user_agent)
         VALUES ($1, $2, $3, $4, NOW(), $5, $6)`,
		sessionID, userID, deviceHash, sessionID, ipAddress, userAgent,
	)
	if err != nil {
		return AuthResult{}, err
	}

	_, err = tx.Exec(ctx,
		`UPDATE users SET last_seen_at = NOW() WHERE id = $1`,
		userID,
	)
	if err != nil {
		return AuthResult{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return AuthResult{}, err
	}

	_ = s.RecalculateTrustLevel(ctx, userID)

	userView, err := s.GetUserByID(ctx, userID)
	if err != nil {
		return AuthResult{}, err
	}

	return AuthResult{
		AccessToken: token,
		ExpiresAt:   exp,
		User:        userView,
	}, nil
}

func (s *Service) Login(ctx context.Context, in LoginInput) (AuthResult, error) {
	email := normalizeEmail(in.Email)
	if email == "" {
		return AuthResult{}, fmt.Errorf("%w: email is required", ErrBadRequest)
	}

	deviceHash := hashString(strings.TrimSpace(in.DeviceFingerprint))

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AuthResult{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	if verifyErr := s.consumeVerificationCodeTx(ctx, tx, email, "login", deviceHash, in.VerificationCode); verifyErr != nil {
		return AuthResult{}, verifyErr
	}

	var userID uuid.UUID
	var isRoot, isBanned bool
	err = tx.QueryRow(ctx,
		`SELECT id, is_root, is_banned FROM users WHERE email = $1`,
		email,
	).Scan(&userID, &isRoot, &isBanned)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AuthResult{}, fmt.Errorf("%w: account not found", ErrNotFound)
		}
		return AuthResult{}, err
	}

	if isBanned {
		return AuthResult{}, fmt.Errorf("%w: account is banned", ErrForbidden)
	}

	sessionID := uuid.New()
	token, exp, issueErr := s.jwt.Issue(userID, sessionID, isRoot)
	if issueErr != nil {
		return AuthResult{}, issueErr
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO user_sessions (id, user_id, device_hash, jwt_id, created_at, ip_address, user_agent)
         VALUES ($1, $2, $3, $4, NOW(), $5, $6)`,
		sessionID, userID, deviceHash, sessionID, in.IPAddress, in.UserAgent,
	)
	if err != nil {
		return AuthResult{}, err
	}

	_, err = tx.Exec(ctx,
		`UPDATE users SET last_seen_at = NOW() WHERE id = $1`,
		userID,
	)
	if err != nil {
		return AuthResult{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return AuthResult{}, err
	}

	_ = s.RecalculateTrustLevel(ctx, userID)

	userView, err := s.GetUserByID(ctx, userID)
	if err != nil {
		return AuthResult{}, err
	}

	return AuthResult{
		AccessToken: token,
		ExpiresAt:   exp,
		User:        userView,
	}, nil
}

func (s *Service) Logout(ctx context.Context, userID, sessionID uuid.UUID) error {
	cmd, err := s.pool.Exec(ctx,
		`UPDATE user_sessions
         SET closed_at = NOW()
         WHERE id = $1 AND user_id = $2 AND closed_at IS NULL`,
		sessionID, userID,
	)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrUnauthorized
	}
	return nil
}

func (s *Service) Heartbeat(ctx context.Context, userID, sessionID uuid.UUID, in HeartbeatInput) error {
	active := in.ActiveSeconds
	if active <= 0 {
		active = 60
	}
	if active > 7200 {
		active = 7200
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	cmd, err := tx.Exec(ctx,
		`UPDATE user_sessions
         SET last_heartbeat_at = NOW(), total_active_seconds = total_active_seconds + $1
         WHERE id = $2 AND user_id = $3 AND closed_at IS NULL`,
		active, sessionID, userID,
	)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrUnauthorized
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO daily_activity (user_id, day, active_seconds, sessions_count)
         VALUES ($1, CURRENT_DATE, $2, 1)
         ON CONFLICT (user_id, day)
         DO UPDATE SET active_seconds = daily_activity.active_seconds + EXCLUDED.active_seconds`,
		userID, active,
	)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx,
		`UPDATE users SET last_seen_at = NOW() WHERE id = $1`,
		userID,
	)
	if err != nil {
		return err
	}

	if err = tx.Commit(ctx); err != nil {
		return err
	}

	return s.RecalculateTrustLevel(ctx, userID)
}

func (s *Service) verifyCaptcha(ctx context.Context, token string) error {
	mode := strings.ToLower(strings.TrimSpace(s.cfg.Runtime.Captcha.Mode))
	switch mode {
	case "", "mock":
		if token == "" {
			return fmt.Errorf("%w: captcha token is required", ErrBadRequest)
		}
		if token != s.cfg.Runtime.Captcha.MockValidToken {
			return fmt.Errorf("%w: captcha validation failed", ErrForbidden)
		}
		return nil
	case "disabled":
		return nil
	case "turnstile":
		return s.verifyTurnstile(ctx, token)
	default:
		return fmt.Errorf("%w: captcha mode %q is not configured on this server", ErrForbidden, mode)
	}
}

func (s *Service) verifyTurnstile(ctx context.Context, token string) error {
	trimmedToken := strings.TrimSpace(token)
	if trimmedToken == "" {
		return fmt.Errorf("%w: captcha token is required", ErrBadRequest)
	}

	secret := strings.TrimSpace(s.cfg.Runtime.Captcha.TurnstileSecret)
	if secret == "" {
		return fmt.Errorf("%w: turnstile secret is not configured", ErrForbidden)
	}

	verifyURL := strings.TrimSpace(s.cfg.Runtime.Captcha.VerifyURL)
	if verifyURL == "" {
		verifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	}

	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", trimmedToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, verifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%w: captcha verification request build failed", ErrForbidden)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: captcha verification request failed", ErrForbidden)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: captcha verification returned status %d", ErrForbidden, resp.StatusCode)
	}

	var payload struct {
		Success    bool     `json:"success"`
		ErrorCodes []string `json:"error-codes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("%w: captcha verification response decode failed", ErrForbidden)
	}
	if !payload.Success {
		return fmt.Errorf("%w: captcha validation failed (%s)", ErrForbidden, strings.Join(payload.ErrorCodes, ","))
	}

	return nil
}

func (s *Service) sendVerificationEmail(email, code, purpose string) error {
	if !s.cfg.Runtime.SMTP.Enabled {
		log.Printf("verification code for %s (%s): %s", email, purpose, code)
		return nil
	}

	authCfg := s.cfg.Runtime.SMTP
	addr := fmt.Sprintf("%s:%d", authCfg.Host, authCfg.Port)
	auth := smtp.PlainAuth("", authCfg.Username, authCfg.Password, authCfg.Host)

	body := fmt.Sprintf("Subject: QGramm verification code\r\n\r\nYour %s code is: %s\r\nIt expires in 15 minutes.", purpose, code)
	return smtp.SendMail(addr, auth, authCfg.From, []string{email}, []byte(body))
}

func (s *Service) consumeVerificationCodeTx(ctx context.Context, tx pgx.Tx, email, purpose, deviceHash, code string) error {
	normalized := strings.TrimSpace(code)
	if normalized == "" {
		return fmt.Errorf("%w: verification code is required", ErrBadRequest)
	}

	var id uuid.UUID
	var expectedHash string
	var attempts int
	err := tx.QueryRow(ctx,
		`SELECT id, code_hash, attempts
         FROM verification_codes
         WHERE email = $1
           AND purpose = $2
           AND device_hash = $3
           AND consumed_at IS NULL
           AND expires_at > NOW()
         ORDER BY created_at DESC
         LIMIT 1
         FOR UPDATE`,
		email, purpose, deviceHash,
	).Scan(&id, &expectedHash, &attempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: verification code not found or expired", ErrBadRequest)
		}
		return err
	}

	incomingHash := hashCode(normalized, s.cfg.Security.VerificationPepper)
	if incomingHash != expectedHash {
		_, _ = tx.Exec(ctx,
			`UPDATE verification_codes SET attempts = attempts + 1 WHERE id = $1`,
			id,
		)
		return fmt.Errorf("%w: invalid verification code", ErrBadRequest)
	}

	_, err = tx.Exec(ctx,
		`UPDATE verification_codes SET consumed_at = NOW() WHERE id = $1`,
		id,
	)
	if err != nil {
		return err
	}

	return nil
}

func (s *Service) ensureDefaultConversationsTx(ctx context.Context, tx pgx.Tx, userID, inviterID uuid.UUID) error {
	var qgrammID uuid.UUID
	if err := tx.QueryRow(ctx,
		`SELECT id FROM users WHERE email = $1`,
		s.cfg.Root.QGrammServiceEmail,
	).Scan(&qgrammID); err != nil {
		return err
	}

	inviterTitle := "Direct"
	_ = tx.QueryRow(ctx,
		`SELECT COALESCE(NULLIF(TRIM(first_name || ' ' || last_name), ''), nickname::text, 'Direct')
         FROM users
         WHERE id = $1`,
		inviterID,
	).Scan(&inviterTitle)

	inviterConversationID, err := s.ensureDirectConversationTx(ctx, tx, userID, inviterID, inviterTitle)
	if err != nil {
		return err
	}

	qgrammConversationID, err := s.ensureDirectConversationTx(ctx, tx, userID, qgrammID, "QGramm")
	if err != nil {
		return err
	}

	_, _ = tx.Exec(ctx,
		`INSERT INTO messages (
            id, conversation_id, sender_user_id, message_type, body_ciphertext, metadata
         ) VALUES ($1,$2,$3,'system',$4,$5)`,
		uuid.New(), inviterConversationID, inviterID,
		"WELCOME_BY_INVITER",
		`{"kind":"welcome","source":"inviter"}`,
	)

	_, _ = tx.Exec(ctx,
		`INSERT INTO messages (
            id, conversation_id, sender_user_id, message_type, body_ciphertext, metadata
         ) VALUES ($1,$2,$3,'system',$4,$5)`,
		uuid.New(), qgrammConversationID, qgrammID,
		"WELCOME_QGRAMM",
		`{"kind":"welcome","source":"qgramm"}`,
	)

	_, _ = tx.Exec(ctx,
		`INSERT INTO system_notifications (id, user_id, kind, payload)
         VALUES ($1, $2, 'tg_verification_prompt', $3::jsonb)`,
		uuid.New(),
		userID,
		`{"title":"Telegram verification","description":"Verify via Telegram bot to unlock Trust Level 2 after 5 days of usage.","placement":"network_tab"}`,
	)

	return nil
}

func (s *Service) ensureDirectConversationTx(ctx context.Context, tx pgx.Tx, userA, userB uuid.UUID, forcedTitle string) (uuid.UUID, error) {
	rows, err := tx.Query(ctx,
		`SELECT c.id
         FROM conversations c
         JOIN conversation_members m1 ON m1.conversation_id = c.id
         JOIN conversation_members m2 ON m2.conversation_id = c.id
         WHERE c.kind = 'direct' AND m1.user_id = $1 AND m2.user_id = $2
         LIMIT 1`,
		userA, userB,
	)
	if err != nil {
		return uuid.Nil, err
	}
	defer rows.Close()

	if rows.Next() {
		var existingID uuid.UUID
		if scanErr := rows.Scan(&existingID); scanErr != nil {
			return uuid.Nil, scanErr
		}
		return existingID, nil
	}

	title := forcedTitle
	if title == "" {
		title = "Direct"
	}

	conversationID := uuid.New()
	isQGramm := forcedTitle == "QGramm"

	_, err = tx.Exec(ctx,
		`INSERT INTO conversations (id, kind, title, is_qgramm, created_by_user_id)
         VALUES ($1, 'direct', $2, $3, $4)`,
		conversationID, title, isQGramm, userA,
	)
	if err != nil {
		return uuid.Nil, err
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO conversation_members (conversation_id, user_id, role)
         VALUES ($1, $2, 'member'), ($1, $3, 'member')`,
		conversationID, userA, userB,
	)
	if err != nil {
		return uuid.Nil, err
	}

	return conversationID, nil
}

func nullableTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}
