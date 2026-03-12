package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"qgramm/backend/internal/services"
)

type Handler struct {
	svc *services.Service
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "qgramm-backend"})
}

func (h *Handler) activateInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code              string `json:"code"`
		DeviceFingerprint string `json:"device_fingerprint"`
	}
	if err := readJSON(r, &req); err != nil {
		handleError(w, err)
		return
	}

	out, err := h.svc.ActivateInvite(r.Context(), services.ActivateInviteInput{
		Code:              req.Code,
		DeviceFingerprint: req.DeviceFingerprint,
	})
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) checkDeviceActivation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceFingerprint string `json:"device_fingerprint"`
	}
	if err := readJSON(r, &req); err != nil {
		handleError(w, err)
		return
	}

	out, err := h.svc.CheckDeviceActivation(r.Context(), req.DeviceFingerprint)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) sendCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email             string `json:"email"`
		Purpose           string `json:"purpose"`
		CaptchaToken      string `json:"captcha_token"`
		DeviceFingerprint string `json:"device_fingerprint"`
	}
	if err := readJSON(r, &req); err != nil {
		handleError(w, err)
		return
	}

	out, err := h.svc.SendVerificationCode(r.Context(), services.SendCodeInput{
		Email:             req.Email,
		Purpose:           req.Purpose,
		CaptchaToken:      req.CaptchaToken,
		DeviceFingerprint: req.DeviceFingerprint,
		IPAddress:         r.RemoteAddr,
	})
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email              string         `json:"email"`
		VerificationCode   string         `json:"verification_code"`
		DeviceFingerprint  string         `json:"device_fingerprint"`
		FirstName          string         `json:"first_name"`
		LastName           string         `json:"last_name"`
		Nickname           string         `json:"nickname"`
		RecoveryCiphertext string         `json:"recovery_ciphertext"`
		RecoveryMeta       map[string]any `json:"recovery_meta"`
	}
	if err := readJSON(r, &req); err != nil {
		handleError(w, err)
		return
	}

	out, err := h.svc.Register(r.Context(), services.RegisterInput{
		Email:              req.Email,
		VerificationCode:   req.VerificationCode,
		DeviceFingerprint:  req.DeviceFingerprint,
		FirstName:          req.FirstName,
		LastName:           req.LastName,
		Nickname:           req.Nickname,
		RecoveryCiphertext: req.RecoveryCiphertext,
		RecoveryMeta:       req.RecoveryMeta,
	}, r.RemoteAddr, r.UserAgent())
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email             string `json:"email"`
		VerificationCode  string `json:"verification_code"`
		DeviceFingerprint string `json:"device_fingerprint"`
	}
	if err := readJSON(r, &req); err != nil {
		handleError(w, err)
		return
	}

	out, err := h.svc.Login(r.Context(), services.LoginInput{
		Email:             req.Email,
		VerificationCode:  req.VerificationCode,
		DeviceFingerprint: req.DeviceFingerprint,
		IPAddress:         r.RemoteAddr,
		UserAgent:         r.UserAgent(),
	})
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	if err := h.svc.Logout(r.Context(), identity.UserID, identity.SessionID); err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) heartbeat(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	var req services.HeartbeatInput
	if err := readJSON(r, &req); err != nil {
		handleError(w, err)
		return
	}

	if err := h.svc.Heartbeat(r.Context(), identity.UserID, identity.SessionID, req); err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	user, err := h.svc.GetUserByID(r.Context(), identity.UserID)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, user)
}

func (h *Handler) updateMe(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	var req struct {
		FirstName  string         `json:"first_name"`
		LastName   string         `json:"last_name"`
		Nickname   string         `json:"nickname"`
		AvatarPath string         `json:"avatar_path"`
		Metadata   map[string]any `json:"metadata"`
	}
	if err := readJSON(r, &req); err != nil {
		handleError(w, err)
		return
	}

	user, err := h.svc.UpdateUserProfile(r.Context(), identity.UserID, services.UserUpdateInput{
		FirstName:  req.FirstName,
		LastName:   req.LastName,
		Nickname:   req.Nickname,
		AvatarPath: req.AvatarPath,
		Metadata:   req.Metadata,
	})
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, user)
}

func (h *Handler) setRecoveryBundle(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	var req services.RecoveryBundle
	if err := readJSON(r, &req); err != nil {
		handleError(w, err)
		return
	}

	if err := h.svc.SetRecoveryBundle(r.Context(), identity.UserID, req); err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) getRecoveryBundle(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	out, err := h.svc.GetRecoveryBundle(r.Context(), identity.UserID)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) listNotifications(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	unreadOnly := strings.EqualFold(r.URL.Query().Get("unread_only"), "true")
	limit := parseIntWithDefault(r.URL.Query().Get("limit"), 100)

	out, err := h.svc.ListNotifications(r.Context(), identity.UserID, unreadOnly, limit)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) markNotificationRead(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	notificationID, err := uuid.Parse(chi.URLParam(r, "notificationID"))
	if err != nil {
		handleError(w, err)
		return
	}

	if err := h.svc.MarkNotificationRead(r.Context(), identity.UserID, notificationID); err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) userByNickname(w http.ResponseWriter, r *http.Request) {
	_, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	nickname := chi.URLParam(r, "nickname")
	user, err := h.svc.GetUserByNickname(r.Context(), nickname)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, user)
}

func (h *Handler) createInvite(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	var req services.CreateInviteInput
	_ = readJSON(r, &req)

	invite, err := h.svc.CreateInvite(r.Context(), identity.UserID, req)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, invite)
}

func (h *Handler) listInvites(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	includeRevoked := strings.EqualFold(r.URL.Query().Get("include_revoked"), "true")
	out, err := h.svc.ListInvites(r.Context(), identity.UserID, includeRevoked)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) inviteGraph(w http.ResponseWriter, r *http.Request) {
	_, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	graph, err := h.svc.GetInviteGraph(r.Context())
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, graph)
}

func (h *Handler) directByNickname(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	var req struct {
		Nickname string `json:"nickname"`
	}
	if err := readJSON(r, &req); err != nil {
		handleError(w, err)
		return
	}

	out, err := h.svc.GetOrCreateDirectByNickname(r.Context(), identity.UserID, req.Nickname)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) listChats(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	out, err := h.svc.ListConversations(r.Context(), identity.UserID)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) listMessages(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	conversationID, err := uuid.Parse(chi.URLParam(r, "conversationID"))
	if err != nil {
		handleError(w, err)
		return
	}

	limit := parseIntWithDefault(r.URL.Query().Get("limit"), 100)

	var before *time.Time
	if raw := strings.TrimSpace(r.URL.Query().Get("before")); raw != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, raw)
		if parseErr == nil {
			before = &parsed
		}
	}

	out, err := h.svc.ListMessages(r.Context(), identity.UserID, conversationID, limit, before)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) markConversationRead(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	conversationID, err := uuid.Parse(chi.URLParam(r, "conversationID"))
	if err != nil {
		handleError(w, err)
		return
	}

	var req struct {
		LastReadMessageID *string `json:"last_read_message_id"`
	}
	if err := readJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		handleError(w, err)
		return
	}

	var lastReadID *uuid.UUID
	if req.LastReadMessageID != nil && strings.TrimSpace(*req.LastReadMessageID) != "" {
		parsed, parseErr := uuid.Parse(*req.LastReadMessageID)
		if parseErr != nil {
			handleError(w, parseErr)
			return
		}
		lastReadID = &parsed
	}

	if err := h.svc.MarkConversationRead(r.Context(), identity.UserID, conversationID, lastReadID); err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) sendMessage(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	conversationID := chi.URLParam(r, "conversationID")

	var req services.SendMessageInput
	if err := readJSON(r, &req); err != nil {
		handleError(w, err)
		return
	}
	req.ConversationID = conversationID

	out, err := h.svc.SendMessage(r.Context(), identity.UserID, req)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) addReaction(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	conversationID, err := uuid.Parse(chi.URLParam(r, "conversationID"))
	if err != nil {
		handleError(w, err)
		return
	}
	messageID, err := uuid.Parse(chi.URLParam(r, "messageID"))
	if err != nil {
		handleError(w, err)
		return
	}

	var req struct {
		Emoji string `json:"emoji"`
	}
	if err := readJSON(r, &req); err != nil {
		handleError(w, err)
		return
	}

	if err := h.svc.AddReaction(r.Context(), identity.UserID, conversationID, messageID, req.Emoji); err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) removeReaction(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	conversationID, err := uuid.Parse(chi.URLParam(r, "conversationID"))
	if err != nil {
		handleError(w, err)
		return
	}
	messageID, err := uuid.Parse(chi.URLParam(r, "messageID"))
	if err != nil {
		handleError(w, err)
		return
	}

	emoji := chi.URLParam(r, "emoji")
	if err := h.svc.RemoveReaction(r.Context(), identity.UserID, conversationID, messageID, emoji); err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) reportMessage(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	conversationID := chi.URLParam(r, "conversationID")
	messageID := chi.URLParam(r, "messageID")

	var req struct {
		Reason string `json:"reason"`
		Note   string `json:"note"`
	}
	if err := readJSON(r, &req); err != nil {
		handleError(w, err)
		return
	}

	out, err := h.svc.CreateReport(r.Context(), identity.UserID, services.ReportInput{
		MessageID:      messageID,
		ConversationID: conversationID,
		Reason:         req.Reason,
		Note:           req.Note,
	})
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) createUpload(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	var req services.CreateUploadInput
	if err := readJSON(r, &req); err != nil {
		handleError(w, err)
		return
	}

	out, err := h.svc.CreateUpload(r.Context(), identity.UserID, req)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) uploadChunk(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	uploadID := chi.URLParam(r, "uploadID")
	chunkIndex := parseIntWithDefault(chi.URLParam(r, "chunkIndex"), -1)

	payload, _ := io.ReadAll(r.Body)

	out, err := h.svc.PutUploadChunk(r.Context(), identity.UserID, services.UploadChunkInput{
		UploadID:   uploadID,
		ChunkIndex: chunkIndex,
		Payload:    payload,
	})
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) completeUpload(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	out, err := h.svc.CompleteUpload(r.Context(), identity.UserID, services.CompleteUploadInput{
		UploadID: chi.URLParam(r, "uploadID"),
	})
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) cancelUpload(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	uploadID, err := uuid.Parse(chi.URLParam(r, "uploadID"))
	if err != nil {
		handleError(w, err)
		return
	}

	out, err := h.svc.CancelUpload(r.Context(), identity.UserID, uploadID)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) getUpload(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	uploadID, err := uuid.Parse(chi.URLParam(r, "uploadID"))
	if err != nil {
		handleError(w, err)
		return
	}

	out, err := h.svc.GetUpload(r.Context(), identity.UserID, uploadID)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) attachmentMeta(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	attachmentID, err := uuid.Parse(chi.URLParam(r, "attachmentID"))
	if err != nil {
		handleError(w, err)
		return
	}

	out, err := h.svc.GetAttachmentByIDForUser(r.Context(), identity.UserID, attachmentID)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) attachmentDownload(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	attachmentID, err := uuid.Parse(chi.URLParam(r, "attachmentID"))
	if err != nil {
		handleError(w, err)
		return
	}

	attachment, absPath, err := h.svc.ResolveAttachmentPathForUser(r.Context(), identity.UserID, attachmentID)
	if err != nil {
		handleError(w, err)
		return
	}
	if _, statErr := os.Stat(absPath); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			handleError(w, fmt.Errorf("%w: attachment file not found", services.ErrNotFound))
			return
		}
		handleError(w, statErr)
		return
	}

	if attachment.MimeType != "" {
		w.Header().Set("Content-Type", attachment.MimeType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", safeContentDispositionFilename(attachment.FileName)))
	http.ServeFile(w, r, absPath)
}

func (h *Handler) startCall(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	var req services.CallStartInput
	if err := readJSON(r, &req); err != nil {
		handleError(w, err)
		return
	}

	out, err := h.svc.StartCall(r.Context(), identity.UserID, req)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) endCall(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	callID, err := uuid.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		handleError(w, err)
		return
	}

	var req services.CallEndInput
	_ = readJSON(r, &req)

	out, err := h.svc.EndCall(r.Context(), identity.UserID, callID, req)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) listCalls(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	limit := parseIntWithDefault(r.URL.Query().Get("limit"), 100)
	out, err := h.svc.ListCalls(r.Context(), identity.UserID, limit)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) websocket(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		handleError(w, err)
		return
	}

	connection := h.svc.Hub().Register(conn, identity.UserID)
	h.svc.NotifyPresenceChanged(r.Context(), identity.UserID, true)
	h.svc.PushPresenceSnapshot(r.Context(), identity.UserID)
	connection.Run(
		func(payload map[string]any) {
			h.svc.HandleRealtimeMessage(r.Context(), identity.UserID, payload)
		},
		func() {
			h.svc.NotifyPresenceChanged(context.Background(), identity.UserID, false)
		},
	)
}

func (h *Handler) adminUsers(w http.ResponseWriter, r *http.Request) {
	limit := parseIntWithDefault(r.URL.Query().Get("limit"), 200)
	out, err := h.svc.ListUsersAdmin(r.Context(), limit)
	if err != nil {
		handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func safeContentDispositionFilename(name string) string {
	clean := strings.TrimSpace(name)
	if clean == "" {
		return "attachment.bin"
	}
	clean = strings.ReplaceAll(clean, "\n", "_")
	clean = strings.ReplaceAll(clean, "\r", "_")
	clean = strings.ReplaceAll(clean, "\"", "")
	return clean
}

func (h *Handler) adminBlockUser(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		handleError(w, err)
		return
	}

	var req struct {
		Block bool `json:"block"`
	}
	if decodeErr := readJSON(r, &req); decodeErr != nil {
		req.Block = true
	}

	if err := h.svc.SetUserBlocked(r.Context(), userID, req.Block); err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) adminSetTrust(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		handleError(w, err)
		return
	}

	var req struct {
		TrustLevel int `json:"trust_level"`
	}
	if err := readJSON(r, &req); err != nil {
		handleError(w, err)
		return
	}

	if err := h.svc.SetUserTrustAdmin(r.Context(), userID, req.TrustLevel); err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) adminSetTelegramVerified(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		handleError(w, err)
		return
	}

	var req struct {
		Verified bool `json:"verified"`
	}
	if decodeErr := readJSON(r, &req); decodeErr != nil {
		req.Verified = true
	}

	if err := h.svc.SetTelegramVerified(r.Context(), userID, req.Verified); err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) adminReports(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limit := parseIntWithDefault(r.URL.Query().Get("limit"), 200)

	out, err := h.svc.ListReportsAdmin(r.Context(), status, limit)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) adminRejectReport(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	reportID, err := uuid.Parse(chi.URLParam(r, "reportID"))
	if err != nil {
		handleError(w, err)
		return
	}

	var req struct {
		Note string `json:"note"`
	}
	_ = readJSON(r, &req)

	out, err := h.svc.RejectReportAdmin(r.Context(), identity.UserID, reportID, req.Note)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) adminBlockReport(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	reportID, err := uuid.Parse(chi.URLParam(r, "reportID"))
	if err != nil {
		handleError(w, err)
		return
	}

	var req struct {
		Note string `json:"note"`
	}
	_ = readJSON(r, &req)

	out, err := h.svc.BlockReportAdmin(r.Context(), identity.UserID, reportID, req.Note)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) adminBroadcast(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	var req services.QGrammBroadcastInput
	if err := readJSON(r, &req); err != nil {
		handleError(w, err)
		return
	}

	out, err := h.svc.BroadcastQGramm(r.Context(), identity.UserID, req)
	if err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) adminRevokeInvite(w http.ResponseWriter, r *http.Request) {
	inviteID, err := uuid.Parse(chi.URLParam(r, "inviteID"))
	if err != nil {
		handleError(w, err)
		return
	}

	if err := h.svc.RevokeInviteAdmin(r.Context(), inviteID); err != nil {
		handleError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func parseIntWithDefault(value string, def int) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return def
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return def
	}
	return parsed
}
