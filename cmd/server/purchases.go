package main

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gozsunday/gater/internal/jsonutil"
	"github.com/gozsunday/gater/internal/qr"
	"github.com/gozsunday/gater/internal/store"
	"github.com/gozsunday/gater/internal/worker"
)

type CreatePurchasePayload struct {
	TierID   string `json:"tier_id" validate:"required"`
	Quantity int    `json:"quantity" validate:"required,gte=1"`
}

type purchaseResponse struct {
	ID        uuid.UUID      `json:"id"`
	Quantity  int            `json:"quantity"`
	Total     int            `json:"total"`
	Status    string         `json:"status"`
	Event     store.Event    `json:"event"`
	Tier      store.Tier     `json:"tier"`
	Tickets   []store.Ticket `json:"tickets"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

func (a *application) createPurchase(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the authenticated user, set on the context by requireAuth
	user, ok := ctx.Value(userCtx).(*store.User)
	if !ok {
		logger.Error("failed to get user from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	var payload CreatePurchasePayload
	if err := jsonutil.Read(w, r, &payload); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	if errs, ok := a.validator.ValidateStruct(&payload); !ok {
		jsonutil.WriteErrors(w, http.StatusUnprocessableEntity, errs)
		return
	}

	// ensure the tierID is a real UUID
	if _, err := uuid.Parse(payload.TierID); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, "invalid tier id")
		return
	}

	tier, err := a.store.Tiers.GetByID(ctx, payload.TierID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			jsonutil.WriteError(w, http.StatusNotFound, "tier not found")
		default:
			logger.Error("failed to get tier", "error", err, "tier_id", payload.TierID)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		}
		return
	}

	event, err := a.store.Events.GetByID(ctx, tier.EventID.String())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			jsonutil.WriteError(w, http.StatusNotFound, "event not found")
		default:
			logger.Error("failed to get event", "error", err, "event_id", tier.EventID)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		}
		return
	}

	if event.Status != "published" {
		jsonutil.WriteError(w, http.StatusConflict, store.ErrEventNotPublished.Error())
		return
	}

	purchase := &store.Purchase{
		ID:       uuid.New(),
		UserID:   user.ID,
		TierID:   tier.ID,
		Quantity: payload.Quantity,
	}

	var tickets []store.Ticket
	for range payload.Quantity {
		ticketID := uuid.New()
		t := store.Ticket{
			ID:         ticketID,
			PurchaseID: purchase.ID,
			TierID:     tier.ID,
			QRToken:    qr.GenerateToken(ticketID, purchase.ID, tier.ID, a.config.TicketSecret),
		}
		tickets = append(tickets, t)
	}

	if err := a.store.Purchases.Create(ctx, purchase, tickets); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			jsonutil.WriteError(w, http.StatusNotFound, "tier not found")
		case errors.Is(err, store.ErrEventNotPublished):
			jsonutil.WriteError(w, http.StatusConflict, store.ErrEventNotPublished.Error())
		case errors.Is(err, store.ErrExceedsMaxPerPurchase):
			jsonutil.WriteError(w, http.StatusUnprocessableEntity, fmt.Sprintf("quantity exceeds the maximum tickets allowed per purchase (%d)", event.MaxTicketsPerPurchase))
		case errors.Is(err, store.ErrInsufficientRemaining):
			jsonutil.WriteError(w, http.StatusConflict, store.ErrInsufficientRemaining.Error())
		default:
			logger.Error("failed to create purchase", "error", err, "tier_id", tier.ID, "user_id", user.ID)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		}
		return
	}

	jsonutil.WriteData(w, http.StatusCreated, purchaseResponse{
		ID:        purchase.ID,
		Quantity:  purchase.Quantity,
		Total:     purchase.Total,
		Status:    purchase.Status,
		Event:     *event,
		Tier:      *tier,
		Tickets:   tickets,
		CreatedAt: purchase.CreatedAt,
		UpdatedAt: purchase.UpdatedAt,
	})
}

func (a *application) listPurchases(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the authenticated user, set on the context by requireAuth
	user, ok := ctx.Value(userCtx).(*store.User)
	if !ok {
		logger.Error("failed to get user from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	// limit has a default of 50, and is clamped to 1-100
	// malformed or out-of-range values silently fall back to the default
	// rather than erroring, so sloppy clients still get sane pages
	limit := 50
	if lq := r.URL.Query().Get("limit"); lq != "" {
		if v, err := strconv.Atoi(lq); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}

	// page starts from 1, and unlike limit, a bad page is rejected loudly
	page := 1
	if pq := r.URL.Query().Get("page"); pq != "" {
		v, err := strconv.Atoi(pq)
		if err != nil || v < 1 {
			jsonutil.WriteError(w, http.StatusBadRequest, "invalid page")
			return
		}
		page = v
	}

	offset := (page - 1) * limit

	purchases, err := a.store.Purchases.ListByUser(ctx, user.ID.String(), limit, offset)
	if err != nil {
		logger.Error("failed to list purchases", "error", err, "user_id", user.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	// for getting the total number of purchases
	total, err := a.store.Purchases.CountByUser(ctx, user.ID.String())
	if err != nil {
		logger.Error("failed to count purchases", "error", err, "user_id", user.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	type returnData struct {
		Message   string                  `json:"message"`
		Purchases []store.PurchaseSummary `json:"purchases"`
		Page      int                     `json:"page"`
		Limit     int                     `json:"limit"`
		Total     int                     `json:"total"`
	}
	jsonutil.WriteData(w, http.StatusOK, returnData{
		Message:   "purchases retrieved successfully",
		Purchases: purchases,
		Page:      page,
		Limit:     limit,
		Total:     total,
	})
}

func (a *application) cancelPurchase(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the authenticated user, set on the context by requireAuth
	user, ok := ctx.Value(userCtx).(*store.User)
	if !ok {
		logger.Error("failed to get user from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	// ensure the {id} route param is a real UUID before hitting the DB
	idParam := chi.URLParam(r, "id")
	if _, err := uuid.Parse(idParam); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, "invalid purchase id")
		return
	}

	purchase, wasSoldOut, err := a.store.Purchases.Cancel(ctx, idParam, user.ID.String())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			jsonutil.WriteError(w, http.StatusNotFound, "purchase not found")
		case errors.Is(err, store.ErrAlreadyCancelled):
			jsonutil.WriteError(w, http.StatusConflict, "purchase has already been cancelled")
		case errors.Is(err, store.ErrEventStarted):
			jsonutil.WriteError(w, http.StatusConflict, "event has already started")
		case errors.Is(err, store.ErrCancellationNotAllowed):
			jsonutil.WriteError(w, http.StatusConflict, "this event does not allow cancellations")
		case errors.Is(err, store.ErrOutsideCancellationWindow):
			jsonutil.WriteError(w, http.StatusConflict, "cancellations are closed for this event")
		default:
			logger.Error("failed to cancel purchase", "error", err, "purchase_id", idParam, "user_id", user.ID)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		}
		return
	}

	if wasSoldOut {
		notifyEntryTask, err := worker.NewNotifyWaitlistEntryTask(purchase.TierID)
		if err != nil {
			logger.Error(
				fmt.Sprintf("failed to create %s task", worker.TypeNotifyWaitlistEntry),
				"error", err,
				"tier_id", purchase.TierID,
			)
		} else {
			taskInfo, err := a.worker.Enqueue(notifyEntryTask)
			if err != nil {
				logger.Error(
					fmt.Sprintf("failed to enqueue %s task", worker.TypeNotifyWaitlistEntry),
					"error", err,
					"tier_id", purchase.TierID,
				)
			} else {
				logger.Info(
					fmt.Sprintf("enqueued %s task", worker.TypeNotifyWaitlistEntry),
					"id", taskInfo.ID,
					"queue", taskInfo.Queue,
				)
			}
		}
	}

	tickets, err := a.store.Purchases.ListTicketsByPurchase(ctx, purchase.ID.String())
	if err != nil {
		logger.Error("failed to list tickets", "error", err, "purchase_id", purchase.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	// the tier and event must exist if the purchase does, so any failure
	// here is corruption, not a client error, therefore 500
	tier, err := a.store.Tiers.GetByID(ctx, purchase.TierID.String())
	if err != nil {
		logger.Error("failed to get purchase tier", "error", err, "tier_id", purchase.TierID, "purchase_id", purchase.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	event, err := a.store.Events.GetByID(ctx, tier.EventID.String())
	if err != nil {
		logger.Error("failed to get purchase event", "error", err, "event_id", tier.EventID, "purchase_id", purchase.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	jsonutil.WriteData(w, http.StatusOK, purchaseResponse{
		ID:        purchase.ID,
		Quantity:  purchase.Quantity,
		Total:     purchase.Total,
		Status:    purchase.Status,
		Event:     *event,
		Tier:      *tier,
		Tickets:   tickets,
		CreatedAt: purchase.CreatedAt,
		UpdatedAt: purchase.UpdatedAt,
	})
}

func (a *application) getPurchase(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the authenticated user, set on the context by requireAuth
	user, ok := ctx.Value(userCtx).(*store.User)
	if !ok {
		logger.Error("failed to get user from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	// ensure the {id} route param is a real UUID before hitting the DB
	idParam := chi.URLParam(r, "id")
	if _, err := uuid.Parse(idParam); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, "invalid purchase id")
		return
	}

	purchase, err := a.store.Purchases.GetByID(ctx, idParam, user.ID.String())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			jsonutil.WriteError(w, http.StatusNotFound, "purchase not found")
		default:
			logger.Error("failed to get purchase", "error", err, "purchase_id", idParam, "user_id", user.ID)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		}
		return
	}

	tickets, err := a.store.Purchases.ListTicketsByPurchase(ctx, purchase.ID.String())
	if err != nil {
		logger.Error("failed to list tickets", "error", err, "purchase_id", purchase.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	// the tier and event must exist if the purchase does, since foreign keys
	// cascade their deletes, so ErrNotFound here would mean corruption,
	// not a client error; treat any failure as a 500
	tier, err := a.store.Tiers.GetByID(ctx, purchase.TierID.String())
	if err != nil {
		logger.Error("failed to get purchase tier", "error", err, "tier_id", purchase.TierID, "purchase_id", purchase.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	event, err := a.store.Events.GetByID(ctx, tier.EventID.String())
	if err != nil {
		logger.Error("failed to get purchase event", "error", err, "event_id", tier.EventID, "purchase_id", purchase.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	jsonutil.WriteData(w, http.StatusOK, purchaseResponse{
		ID:        purchase.ID,
		Quantity:  purchase.Quantity,
		Total:     purchase.Total,
		Status:    purchase.Status,
		Event:     *event,
		Tier:      *tier,
		Tickets:   tickets,
		CreatedAt: purchase.CreatedAt,
		UpdatedAt: purchase.UpdatedAt,
	})
}
