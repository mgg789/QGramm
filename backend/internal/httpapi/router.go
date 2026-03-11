package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"qgramm/backend/internal/services"
)

func NewRouter(svc *services.Service) http.Handler {
	h := &Handler{svc: svc}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/healthz", h.health)

	r.Route("/v1", func(r chi.Router) {
		r.Post("/auth/invites/activate", h.activateInvite)
		r.Post("/auth/device/check", h.checkDeviceActivation)
		r.Post("/auth/send-code", h.sendCode)
		r.Post("/auth/register", h.register)
		r.Post("/auth/login", h.login)

		r.Group(func(r chi.Router) {
			r.Use(authMiddleware(svc))

			r.Post("/auth/logout", h.logout)
			r.Post("/auth/heartbeat", h.heartbeat)
			r.Get("/auth/ws", h.websocket)

			r.Get("/me", h.me)
			r.Patch("/me", h.updateMe)
			r.Put("/me/recovery-bundle", h.setRecoveryBundle)
			r.Get("/me/recovery-bundle", h.getRecoveryBundle)

			r.Get("/notifications", h.listNotifications)
			r.Post("/notifications/{notificationID}/read", h.markNotificationRead)

			r.Get("/users/by-nickname/{nickname}", h.userByNickname)

			r.Post("/invites", h.createInvite)
			r.Get("/invites", h.listInvites)
			r.Get("/invites/graph", h.inviteGraph)

			r.Post("/chats/direct/by-nickname", h.directByNickname)
			r.Get("/chats", h.listChats)
			r.Get("/chats/{conversationID}/messages", h.listMessages)
			r.Post("/chats/{conversationID}/messages", h.sendMessage)
			r.Post("/chats/{conversationID}/messages/{messageID}/reactions", h.addReaction)
			r.Delete("/chats/{conversationID}/messages/{messageID}/reactions/{emoji}", h.removeReaction)
			r.Post("/chats/{conversationID}/messages/{messageID}/report", h.reportMessage)

			r.Post("/uploads", h.createUpload)
			r.Put("/uploads/{uploadID}/chunks/{chunkIndex}", h.uploadChunk)
			r.Post("/uploads/{uploadID}/complete", h.completeUpload)
			r.Post("/uploads/{uploadID}/cancel", h.cancelUpload)
			r.Get("/uploads/{uploadID}", h.getUpload)
			r.Get("/attachments/{attachmentID}", h.attachmentMeta)
			r.Get("/attachments/{attachmentID}/download", h.attachmentDownload)

			r.Post("/calls/start", h.startCall)
			r.Post("/calls/{callID}/end", h.endCall)
			r.Get("/calls", h.listCalls)

			r.Route("/admin", func(r chi.Router) {
				r.Use(rootMiddleware)
				r.Get("/users", h.adminUsers)
				r.Post("/users/{userID}/block", h.adminBlockUser)
				r.Post("/users/{userID}/trust", h.adminSetTrust)
				r.Post("/users/{userID}/telegram-verified", h.adminSetTelegramVerified)

				r.Get("/reports", h.adminReports)
				r.Post("/reports/{reportID}/reject", h.adminRejectReport)
				r.Post("/reports/{reportID}/block", h.adminBlockReport)

				r.Post("/qgramm/broadcast", h.adminBroadcast)
				r.Post("/invites/{inviteID}/revoke", h.adminRevokeInvite)
			})
		})
	})

	return r
}
