package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"qgramm/backend/internal/realtime"
)

type ConversationView struct {
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	Title          string    `json:"title"`
	IsQGramm       bool      `json:"is_qgramm"`
	UpdatedAt      time.Time `json:"updated_at"`
	ParticipantIDs []string  `json:"participant_ids"`
	UnreadCount    int       `json:"unread_count"`
}

type MessageView struct {
	ID                          string                `json:"id"`
	ConversationID              string                `json:"conversation_id"`
	SenderUserID                *string               `json:"sender_user_id,omitempty"`
	MessageType                 string                `json:"message_type"`
	BodyCiphertext              string                `json:"body_ciphertext,omitempty"`
	BodyNonce                   string                `json:"body_nonce,omitempty"`
	BodyTag                     string                `json:"body_tag,omitempty"`
	QuotedCiphertext            string                `json:"quoted_ciphertext,omitempty"`
	QuotedNonce                 string                `json:"quoted_nonce,omitempty"`
	QuotedTag                   string                `json:"quoted_tag,omitempty"`
	ReplyToMessageID            *string               `json:"reply_to_message_id,omitempty"`
	ForwardedFromMessageID      *string               `json:"forwarded_from_message_id,omitempty"`
	ForwardedFromConversationID *string               `json:"forwarded_from_conversation_id,omitempty"`
	AttachmentID                *string               `json:"attachment_id,omitempty"`
	CreatedAt                   time.Time             `json:"created_at"`
	ReportCount                 int                   `json:"report_count"`
	Reactions                   []MessageReactionView `json:"reactions,omitempty"`
	Metadata                    map[string]any        `json:"metadata,omitempty"`
}

type MessageReactionView struct {
	Emoji   string   `json:"emoji"`
	UserIDs []string `json:"user_ids"`
}

func (s *Service) GetOrCreateDirectByNickname(ctx context.Context, requesterID uuid.UUID, nickname string) (ConversationView, error) {
	target, err := s.GetUserByNickname(ctx, nickname)
	if err != nil {
		return ConversationView{}, err
	}

	targetID, parseErr := uuid.Parse(target.ID)
	if parseErr != nil {
		return ConversationView{}, parseErr
	}
	if requesterID == targetID {
		return ConversationView{}, fmt.Errorf("%w: cannot create direct chat with yourself", ErrBadRequest)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ConversationView{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	title := fmt.Sprintf("%s %s", target.FirstName, target.LastName)
	title = strings.TrimSpace(title)
	if title == "" {
		title = target.Nickname
	}

	conversationID, err := s.ensureDirectConversationTx(ctx, tx, requesterID, targetID, title)
	if err != nil {
		return ConversationView{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return ConversationView{}, err
	}

	conversations, err := s.ListConversations(ctx, requesterID)
	if err != nil {
		return ConversationView{}, err
	}

	for _, c := range conversations {
		if c.ID == conversationID.String() {
			return c, nil
		}
	}

	return ConversationView{}, ErrNotFound
}

func (s *Service) ListConversations(ctx context.Context, userID uuid.UUID) ([]ConversationView, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT c.id::text, c.kind, c.title, c.is_qgramm, c.updated_at,
                COALESCE(me.unread_count, 0),
                ARRAY(
                    SELECT cm2.user_id::text
                    FROM conversation_members cm2
                    WHERE cm2.conversation_id = c.id
                    ORDER BY cm2.joined_at ASC
                ) AS participant_ids
         FROM conversations c
         JOIN conversation_members me ON me.conversation_id = c.id
         WHERE me.user_id = $1
         ORDER BY c.updated_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ConversationView, 0)
	for rows.Next() {
		var c ConversationView
		if scanErr := rows.Scan(
			&c.ID,
			&c.Kind,
			&c.Title,
			&c.IsQGramm,
			&c.UpdatedAt,
			&c.UnreadCount,
			&c.ParticipantIDs,
		); scanErr != nil {
			return nil, scanErr
		}
		out = append(out, c)
	}

	return out, rows.Err()
}

func (s *Service) ListMessages(ctx context.Context, userID uuid.UUID, conversationID uuid.UUID, limit int, before *time.Time) ([]MessageView, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}

	if err := s.ensureConversationMembership(ctx, userID, conversationID); err != nil {
		return nil, err
	}

	query := `
SELECT id::text,
       conversation_id::text,
       sender_user_id::text,
       message_type,
       COALESCE(body_ciphertext, ''),
       COALESCE(body_nonce, ''),
       COALESCE(body_tag, ''),
       COALESCE(quoted_ciphertext, ''),
       COALESCE(quoted_nonce, ''),
       COALESCE(quoted_tag, ''),
       reply_to_message_id::text,
       forwarded_from_message_id::text,
       forwarded_from_conversation_id::text,
       attachment_id::text,
       created_at,
       report_count,
       metadata
FROM messages
WHERE conversation_id = $1
`

	args := []any{conversationID}
	if before != nil {
		query += " AND created_at < $2"
		args = append(args, before.UTC())
	}

	query += " ORDER BY created_at DESC LIMIT "
	query += fmt.Sprintf("%d", limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]MessageView, 0, limit)
	for rows.Next() {
		var m MessageView
		var senderID, replyTo, fwdMsg, fwdConv, attachmentID sql.NullString
		var metadataRaw []byte

		if scanErr := rows.Scan(
			&m.ID,
			&m.ConversationID,
			&senderID,
			&m.MessageType,
			&m.BodyCiphertext,
			&m.BodyNonce,
			&m.BodyTag,
			&m.QuotedCiphertext,
			&m.QuotedNonce,
			&m.QuotedTag,
			&replyTo,
			&fwdMsg,
			&fwdConv,
			&attachmentID,
			&m.CreatedAt,
			&m.ReportCount,
			&metadataRaw,
		); scanErr != nil {
			return nil, scanErr
		}

		if senderID.Valid {
			v := senderID.String
			m.SenderUserID = &v
		}
		if replyTo.Valid {
			v := replyTo.String
			m.ReplyToMessageID = &v
		}
		if fwdMsg.Valid {
			v := fwdMsg.String
			m.ForwardedFromMessageID = &v
		}
		if fwdConv.Valid {
			v := fwdConv.String
			m.ForwardedFromConversationID = &v
		}
		if attachmentID.Valid {
			v := attachmentID.String
			m.AttachmentID = &v
		}
		m.Metadata = parseJSONMap(metadataRaw)
		out = append(out, m)
	}

	// Return oldest->newest to simplify client rendering.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}

	reactionsByMessage, err := s.loadMessageReactions(ctx, out)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Reactions = reactionsByMessage[out[i].ID]
	}

	return out, rows.Err()
}

func (s *Service) SendMessage(ctx context.Context, senderID uuid.UUID, in SendMessageInput) (MessageView, error) {
	conversationID, err := uuid.Parse(in.ConversationID)
	if err != nil {
		return MessageView{}, fmt.Errorf("%w: invalid conversation_id", ErrBadRequest)
	}

	if err = s.ensureConversationMembership(ctx, senderID, conversationID); err != nil {
		return MessageView{}, err
	}

	messageType := strings.TrimSpace(in.MessageType)
	switch messageType {
	case "text", "voice_note", "circular_video", "media", "file", "system":
	default:
		return MessageView{}, fmt.Errorf("%w: invalid message_type", ErrBadRequest)
	}

	if messageType == "text" && strings.TrimSpace(in.BodyCiphertext) == "" {
		return MessageView{}, fmt.Errorf("%w: body_ciphertext is required for text", ErrBadRequest)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MessageView{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	messageID := uuid.New()
	metadataBytes, _ := json.Marshal(in.Metadata)

	var replyToID, fwdMsgID, fwdConvID, attachmentUUID *uuid.UUID
	if in.ReplyToMessageID != nil && *in.ReplyToMessageID != "" {
		parsed, parseErr := uuid.Parse(*in.ReplyToMessageID)
		if parseErr != nil {
			return MessageView{}, fmt.Errorf("%w: invalid reply_to_message_id", ErrBadRequest)
		}
		replyToID = &parsed
	}
	if in.ForwardedFromMessageID != nil && *in.ForwardedFromMessageID != "" {
		parsed, parseErr := uuid.Parse(*in.ForwardedFromMessageID)
		if parseErr != nil {
			return MessageView{}, fmt.Errorf("%w: invalid forwarded_from_message_id", ErrBadRequest)
		}
		fwdMsgID = &parsed
	}
	if in.ForwardedFromConversationID != nil && *in.ForwardedFromConversationID != "" {
		parsed, parseErr := uuid.Parse(*in.ForwardedFromConversationID)
		if parseErr != nil {
			return MessageView{}, fmt.Errorf("%w: invalid forwarded_from_conversation_id", ErrBadRequest)
		}
		fwdConvID = &parsed
	}
	if in.AttachmentID != nil && *in.AttachmentID != "" {
		parsed, parseErr := uuid.Parse(*in.AttachmentID)
		if parseErr != nil {
			return MessageView{}, fmt.Errorf("%w: invalid attachment_id", ErrBadRequest)
		}
		attachmentUUID = &parsed
	}

	expectedAttachmentKind := ""
	switch messageType {
	case "file":
		expectedAttachmentKind = "file"
	case "media":
		expectedAttachmentKind = "media"
	case "voice_note":
		expectedAttachmentKind = "voice_note"
	case "circular_video":
		expectedAttachmentKind = "circular_video"
	}

	if expectedAttachmentKind != "" && attachmentUUID == nil {
		return MessageView{}, fmt.Errorf("%w: attachment_id is required for message_type %s", ErrBadRequest, messageType)
	}
	if expectedAttachmentKind == "" && attachmentUUID != nil {
		return MessageView{}, fmt.Errorf("%w: attachment_id is not allowed for message_type %s", ErrBadRequest, messageType)
	}
	if attachmentUUID != nil {
		var ownerID uuid.UUID
		var attachmentKind string
		err = tx.QueryRow(ctx,
			`SELECT owner_user_id, kind
             FROM attachments
             WHERE id = $1`,
			*attachmentUUID,
		).Scan(&ownerID, &attachmentKind)
		if err != nil {
			if err == pgx.ErrNoRows {
				return MessageView{}, fmt.Errorf("%w: attachment not found", ErrNotFound)
			}
			return MessageView{}, err
		}
		if ownerID != senderID {
			return MessageView{}, fmt.Errorf("%w: attachment does not belong to sender", ErrForbidden)
		}
		if expectedAttachmentKind != "" && attachmentKind != expectedAttachmentKind {
			return MessageView{}, fmt.Errorf("%w: attachment kind %s does not match message_type %s", ErrBadRequest, attachmentKind, messageType)
		}
	}

	if replyToID != nil {
		var exists bool
		err = tx.QueryRow(ctx,
			`SELECT EXISTS (
                SELECT 1
                FROM messages
                WHERE id = $1
                  AND conversation_id = $2
            )`,
			*replyToID,
			conversationID,
		).Scan(&exists)
		if err != nil {
			return MessageView{}, err
		}
		if !exists {
			return MessageView{}, fmt.Errorf("%w: reply_to_message_id does not belong to conversation", ErrBadRequest)
		}
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO messages (
            id,
            conversation_id,
            sender_user_id,
            message_type,
            body_ciphertext,
            body_nonce,
            body_tag,
            quoted_ciphertext,
            quoted_nonce,
            quoted_tag,
            reply_to_message_id,
            forwarded_from_message_id,
            forwarded_from_conversation_id,
            attachment_id,
            metadata
         ) VALUES (
            $1,$2,$3,$4,
            NULLIF($5,''), NULLIF($6,''), NULLIF($7,''),
            NULLIF($8,''), NULLIF($9,''), NULLIF($10,''),
            $11,$12,$13,$14,
            COALESCE($15::jsonb, '{}'::jsonb)
         )`,
		messageID,
		conversationID,
		senderID,
		messageType,
		in.BodyCiphertext,
		in.BodyNonce,
		in.BodyTag,
		in.QuotedCiphertext,
		in.QuotedNonce,
		in.QuotedTag,
		replyToID,
		fwdMsgID,
		fwdConvID,
		attachmentUUID,
		string(metadataBytes),
	)
	if err != nil {
		return MessageView{}, err
	}

	_, err = tx.Exec(ctx,
		`UPDATE conversations SET updated_at = NOW() WHERE id = $1`,
		conversationID,
	)
	if err != nil {
		return MessageView{}, err
	}

	_, err = tx.Exec(ctx,
		`UPDATE conversation_members
         SET unread_count = CASE WHEN user_id = $2 THEN unread_count ELSE unread_count + 1 END
         WHERE conversation_id = $1`,
		conversationID,
		senderID,
	)
	if err != nil {
		return MessageView{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return MessageView{}, err
	}

	var result MessageView
	resultRows, listErr := s.ListMessages(ctx, senderID, conversationID, 1, nil)
	if listErr == nil && len(resultRows) > 0 {
		result = resultRows[len(resultRows)-1]
	} else {
		result = MessageView{
			ID:             messageID.String(),
			ConversationID: conversationID.String(),
			MessageType:    messageType,
			CreatedAt:      time.Now().UTC(),
			Metadata:       in.Metadata,
		}
	}

	memberIDs, membersErr := s.conversationMembers(ctx, conversationID)
	if membersErr == nil {
		event := realtimeEventMessageCreated(result)
		s.hub.BroadcastToUsers(memberIDs, event)
	}

	return result, nil
}

func (s *Service) loadMessageReactions(ctx context.Context, messages []MessageView) (map[string][]MessageReactionView, error) {
	reactions := make(map[string][]MessageReactionView, len(messages))
	if len(messages) == 0 {
		return reactions, nil
	}

	messageIDs := make([]uuid.UUID, 0, len(messages))
	for _, m := range messages {
		id, err := uuid.Parse(m.ID)
		if err != nil {
			continue
		}
		messageIDs = append(messageIDs, id)
	}
	if len(messageIDs) == 0 {
		return reactions, nil
	}

	rows, err := s.pool.Query(ctx,
		`SELECT message_id::text, emoji, ARRAY_AGG(user_id::text ORDER BY created_at ASC) AS user_ids
         FROM message_reactions
         WHERE message_id = ANY($1)
         GROUP BY message_id, emoji
         ORDER BY message_id ASC, emoji ASC`,
		messageIDs,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var messageID string
		var emoji string
		var userIDs []string
		if scanErr := rows.Scan(&messageID, &emoji, &userIDs); scanErr != nil {
			return nil, scanErr
		}
		reactions[messageID] = append(reactions[messageID], MessageReactionView{
			Emoji:   emoji,
			UserIDs: userIDs,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return reactions, nil
}

func (s *Service) AddReaction(ctx context.Context, userID uuid.UUID, conversationID, messageID uuid.UUID, emoji string) error {
	if strings.TrimSpace(emoji) == "" {
		return fmt.Errorf("%w: emoji is required", ErrBadRequest)
	}
	if err := s.ensureConversationMembership(ctx, userID, conversationID); err != nil {
		return err
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO message_reactions (message_id, emoji, user_id)
         VALUES ($1, $2, $3)
         ON CONFLICT (message_id, emoji, user_id) DO NOTHING`,
		messageID,
		strings.TrimSpace(emoji),
		userID,
	)
	if err != nil {
		return err
	}

	memberIDs, membersErr := s.conversationMembers(ctx, conversationID)
	if membersErr == nil {
		s.hub.BroadcastToUsers(memberIDs, realtimeEvent("reaction.updated", map[string]any{
			"conversation_id": conversationID.String(),
			"message_id":      messageID.String(),
			"emoji":           emoji,
			"user_id":         userID.String(),
			"action":          "add",
		}))
	}

	return nil
}

func (s *Service) RemoveReaction(ctx context.Context, userID uuid.UUID, conversationID, messageID uuid.UUID, emoji string) error {
	if err := s.ensureConversationMembership(ctx, userID, conversationID); err != nil {
		return err
	}

	_, err := s.pool.Exec(ctx,
		`DELETE FROM message_reactions WHERE message_id = $1 AND emoji = $2 AND user_id = $3`,
		messageID,
		strings.TrimSpace(emoji),
		userID,
	)
	if err != nil {
		return err
	}

	memberIDs, membersErr := s.conversationMembers(ctx, conversationID)
	if membersErr == nil {
		s.hub.BroadcastToUsers(memberIDs, realtimeEvent("reaction.updated", map[string]any{
			"conversation_id": conversationID.String(),
			"message_id":      messageID.String(),
			"emoji":           emoji,
			"user_id":         userID.String(),
			"action":          "remove",
		}))
	}

	return nil
}

func (s *Service) ensureConversationMembership(ctx context.Context, userID, conversationID uuid.UUID) error {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (
            SELECT 1
            FROM conversation_members
            WHERE conversation_id = $1 AND user_id = $2
         )`,
		conversationID,
		userID,
	).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: conversation access denied", ErrForbidden)
	}
	return nil
}

func (s *Service) conversationMembers(ctx context.Context, conversationID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id FROM conversation_members WHERE conversation_id = $1`,
		conversationID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	members := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, scanErr
		}
		members = append(members, id)
	}

	return members, rows.Err()
}

func realtimeEvent(kind string, payload map[string]any) realtime.Event {
	return realtime.Event{
		Type:      kind,
		Timestamp: time.Now().UTC(),
		Payload:   payload,
	}
}

func realtimeEventMessageCreated(message MessageView) realtime.Event {
	return realtimeEvent("message.created", map[string]any{
		"message": message,
	})
}
