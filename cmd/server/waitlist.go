package main

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gozsunday/gater/internal/jsonutil"
	"github.com/gozsunday/gater/internal/store"
)

func (a *application) joinWaitlist(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the authenticated user, set on the context by requireAuth
	user, ok := ctx.Value(userCtx).(*store.User)
	if !ok {
		logger.Error("failed to get user from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	event := a.getPublicEvent(w, r)
	if event == nil {
		// error response already written in getPublicEvent
		return
	}

	if event.Status == "ended" {
		jsonutil.WriteError(w, http.StatusConflict, "this event has ended")
		return
	}

	// ensure the {tierId} route param is a real UUID
	tierIDParam := chi.URLParam(r, "tierId")
	tierID, err := uuid.Parse(tierIDParam)
	if err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, "invalid tier id")
		return
	}

	tier, err := a.store.Tiers.GetByID(ctx, tierID.String())
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			jsonutil.WriteError(w, http.StatusNotFound, "tier not found")
		default:
			logger.Error("failed to get tier", "error", err, "tier_id", tierID)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		}
		return
	}

	// the tier must belong to the event in the route
	if tier.EventID != event.ID {
		jsonutil.WriteError(w, http.StatusNotFound, "tier not found")
		return
	}

	// no point queueing for stock that exists
	if tier.Status != "sold_out" {
		jsonutil.WriteError(w, http.StatusConflict,
			"you can only join the waitlist for sold out tiers")
		return
	}

	holds, err := a.store.Purchases.HasConfirmedPurchase(ctx, user.ID.String(), tier.ID.String())
	if err != nil {
		logger.Error("failed to check existing purchase", "error", err, "user_id", user.ID, "tier_id", tier.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}
	if holds {
		jsonutil.WriteError(w, http.StatusConflict, "you already have tickets for this tier")
		return
	}

	entry := &store.WaitlistEntry{
		UserID: user.ID,
		TierID: tier.ID,
	}

	if err := a.store.Waitlist.Create(ctx, entry); err != nil {
		switch {
		case errors.Is(err, store.ErrConflict):
			jsonutil.WriteError(w, http.StatusConflict, "you are already on the waitlist for this tier")
		default:
			logger.Error("failed to create waitlist entry", "error", err, "user_id", user.ID, "tier_id", tier.ID)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		}
		return
	}

	type returnData struct {
		Message string               `json:"message"`
		Entry   *store.WaitlistEntry `json:"waitlist_entry"`
	}
	jsonutil.WriteData(w, http.StatusCreated, returnData{
		Message: "you have been added to the waitlist",
		Entry:   entry,
	})
}

func (a *application) leaveWaitlist(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the authenticated user, set on the context by requireAuth
	user, ok := ctx.Value(userCtx).(*store.User)
	if !ok {
		logger.Error("failed to get user from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	// ensure both route params are real UUIDs before hitting the DB
	idParam := chi.URLParam(r, "id")
	if _, err := uuid.Parse(idParam); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, "invalid event id")
		return
	}
	tierIDParam := chi.URLParam(r, "tierId")
	tierID, err := uuid.Parse(tierIDParam)
	if err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, "invalid tier id")
		return
	}

	if err := a.store.Waitlist.DeleteByUserAndTier(ctx, user.ID.String(), tierID.String()); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			jsonutil.WriteError(w, http.StatusNotFound, "you are not on the waitlist for this tier")
		default:
			logger.Error("failed to delete waitlist entry", "error", err, "user_id", user.ID, "tier_id", tierID)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		}
		return
	}

	type returnData struct {
		Message string `json:"message"`
	}
	jsonutil.WriteData(w, http.StatusOK, returnData{
		Message: "you have been removed from the waitlist",
	})
}

func (a *application) getWaitlist(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the event, loaded by requireEventOrganizer
	event, ok := ctx.Value(eventCtx).(*store.Event)
	if !ok {
		logger.Error("failed to get event from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	entries, err := a.store.Waitlist.ListByEvent(ctx, event.ID.String())
	if err != nil {
		logger.Error("failed to list waitlist", "error", err, "event_id", event.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	type returnData struct {
		Message  string                  `json:"message"`
		Waitlist []store.WaitlistSummary `json:"waitlist"`
	}
	jsonutil.WriteData(w, http.StatusOK, returnData{
		Message:  "waitlist retrieved successfully",
		Waitlist: entries,
	})
}
