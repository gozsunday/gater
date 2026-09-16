package main

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gozsunday/gater/internal/jsonutil"
	"github.com/gozsunday/gater/internal/store"
)

type CreateTierPayload struct {
	Name     string `json:"name" validate:"required,max=255"`
	Price    *int   `json:"price" validate:"omitempty,gte=0"`
	Quantity *int   `json:"quantity" validate:"omitempty,gt=0"`
}

func (a *application) listTiers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	event := a.getPublicEvent(w, r)
	if event == nil {
		return // error response already written in getPublicEvent
	}

	tiers, err := a.store.Tiers.ListByEvent(ctx, event.ID.String())
	if err != nil {
		logger.Error("failed to list tiers", "error", err, "event_id", event.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	type returnData struct {
		Message string        `json:"message"`
		Tiers   []*store.Tier `json:"tiers"`
	}
	jsonutil.WriteData(w, http.StatusOK, returnData{
		Message: "tiers retrieved successfully",
		Tiers:   tiers,
	})
}

type UpdateTierPayload struct {
	Name     *string `json:"name" validate:"omitempty,max=255"`
	Price    *int    `json:"price" validate:"omitempty,gte=0"`
	Quantity *int    `json:"quantity" validate:"omitempty,gte=1"`
}

func (a *application) updateTier(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the event, loaded by requireEventOrganizer
	event, ok := ctx.Value(eventCtx).(*store.Event)
	if !ok {
		logger.Error("failed to get event from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	// cancelled and ended events are frozen
	if event.Status == "cancelled" || event.Status == "ended" {
		jsonutil.WriteError(w, http.StatusConflict, "event can no longer be updated")
		return
	}

	// ensure the {tierId} route param is a real UUID
	tierID := chi.URLParam(r, "tierId")
	if _, err := uuid.Parse(tierID); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, "invalid tier id")
		return
	}

	tier, err := a.store.Tiers.GetByID(ctx, tierID)
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

	var payload UpdateTierPayload
	if err := jsonutil.Read(w, r, &payload); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	if errs, ok := a.validator.ValidateStruct(&payload); !ok {
		jsonutil.WriteErrors(w, http.StatusUnprocessableEntity, errs)
		return
	}

	// merge the non-nil fields onto the current tier
	if payload.Name != nil {
		tier.Name = *payload.Name
	}
	if payload.Price != nil {
		tier.Price = *payload.Price
	}

	// quantity changes recompute remaining; the new quantity can never
	// drop below the number of tickets already sold
	if payload.Quantity != nil {
		sold := tier.Quantity - tier.Remaining

		if *payload.Quantity < sold {
			jsonutil.WriteError(w, http.StatusUnprocessableEntity,
				fmt.Sprintf("quantity cannot be reduced below the %d tickets already sold", sold))
			return
		}

		// if the event has a capacity cap, the total tier inventory
		// (existing tiers + this one) must not exceed it
		if event.Capacity != nil {
			existingTotal, err := a.store.Tiers.SumQuantityByEvent(ctx, event.ID.String())
			if err != nil {
				logger.Error("failed to sum tier quantities", "error", err, "event_id", event.ID)
				jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
				return
			}

			// net out this tier's old quantity, then add the new one
			newTotal := existingTotal - tier.Quantity + *payload.Quantity
			if newTotal > *event.Capacity {
				jsonutil.WriteError(w, http.StatusBadRequest,
					fmt.Sprintf("total tier capacity (%d) would exceed event capacity (%d)",
						newTotal, *event.Capacity))
				return
			}
		}

		tier.Quantity = *payload.Quantity
		tier.Remaining = tier.Quantity - sold

		// a tier is back on sale if it has inventory, sold out if it doesn't
		if tier.Remaining > 0 {
			tier.Status = "available"
		} else {
			tier.Status = "sold_out"
		}
	}

	if err := a.store.Tiers.Update(ctx, tier); err != nil {
		logger.Error("failed to update tier", "error", err, "tier_id", tier.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	// the event is no longer fully sold out once any tier has inventory again
	if event.Status == "sold_out" && tier.Remaining > 0 {
		if err := a.store.Events.SetStatus(ctx, event.ID.String(), "sold_out", "published"); err != nil {
			logger.Error("failed to restore event to published", "error", err, "event_id", event.ID)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
			return
		}
	}

	type returnData struct {
		Message string      `json:"message"`
		Tier    *store.Tier `json:"tier"`
	}
	jsonutil.WriteData(w, http.StatusOK, returnData{
		Message: "tier updated successfully",
		Tier:    tier,
	})
}

func (a *application) deleteTier(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the event, loaded by requireEventOrganizer
	event, ok := ctx.Value(eventCtx).(*store.Event)
	if !ok {
		logger.Error("failed to get event from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	if event.Status != "draft" {
		jsonutil.WriteError(w, http.StatusConflict, "tiers can only be deleted from draft events")
		return
	}

	// ensure the {tierId} route param is a real UUID
	tierID := chi.URLParam(r, "tierId")
	if _, err := uuid.Parse(tierID); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, "invalid tier id")
		return
	}

	if err := a.store.Tiers.Delete(ctx, tierID, event.ID.String()); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			jsonutil.WriteError(w, http.StatusNotFound, "tier not found")
		default:
			logger.Error("failed to delete tier", "error", err, "tier_id", tierID)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		}
		return
	}

	type returnData struct {
		Message string `json:"message"`
	}
	jsonutil.WriteData(w, http.StatusOK, returnData{
		Message: "tier deleted successfully",
	})
}

func (a *application) createTier(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the event, loaded by requireEventOrganizer
	event, ok := ctx.Value(eventCtx).(*store.Event)
	if !ok {
		logger.Error("failed to get event from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	var payload CreateTierPayload
	if err := jsonutil.Read(w, r, &payload); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	if errs, ok := a.validator.ValidateStruct(&payload); !ok {
		jsonutil.WriteErrors(w, http.StatusUnprocessableEntity, errs)
		return
	}

	// tiers can only be added while the event is still a draft
	if event.Status != "draft" {
		jsonutil.WriteError(w, http.StatusConflict, "tiers can only be added to draft events")
		return
	}

	if payload.Price == nil {
		jsonutil.WriteError(w, http.StatusUnprocessableEntity, "price is required")
		return
	}
	if payload.Quantity == nil {
		jsonutil.WriteError(w, http.StatusUnprocessableEntity, "quantity is required")
		return
	}

	// if the event has a capacity cap, the total tier inventory
	// (existing tiers + this new one) must not exceed it
	if event.Capacity != nil {
		existingTotal, err := a.store.Tiers.SumQuantityByEvent(ctx, event.ID.String())
		if err != nil {
			logger.Error("failed to sum tier quantities", "error", err, "event_id", event.ID)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
			return
		}

		if existingTotal+*payload.Quantity > *event.Capacity {
			jsonutil.WriteError(w, http.StatusBadRequest,
				fmt.Sprintf("total tier capacity (%d) would exceed event capacity (%d)",
					existingTotal+*payload.Quantity, *event.Capacity))
			return
		}
	}

	tier := &store.Tier{
		EventID:   event.ID,
		Name:      payload.Name,
		Price:     *payload.Price,
		Quantity:  *payload.Quantity,
		Remaining: *payload.Quantity, // a brand-new tier is fully in stock
	}

	if err := a.store.Tiers.Create(ctx, tier); err != nil {
		logger.Error("failed to create tier", "error", err, "event_id", event.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	type returnData struct {
		Message string      `json:"message"`
		Tier    *store.Tier `json:"tier"`
	}
	jsonutil.WriteData(w, http.StatusCreated, returnData{
		Message: "tier created successfully",
		Tier:    tier,
	})
}
