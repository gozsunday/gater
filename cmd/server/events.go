package main

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gozsunday/gater/internal/cursor"
	"github.com/gozsunday/gater/internal/jsonutil"
	"github.com/gozsunday/gater/internal/store"
	"github.com/gozsunday/gater/internal/worker"
)

type CreateEventPayload struct {
	Name                    string    `json:"name" validate:"required,max=255"`
	Description             *string   `json:"description"`
	Location                string    `json:"location" validate:"required,max=255"`
	StartsAt                time.Time `json:"starts_at" validate:"required"`
	EndsAt                  time.Time `json:"ends_at" validate:"required"`
	Capacity                *int      `json:"capacity" validate:"omitempty,gte=1"`
	CancellationAllowed     *bool     `json:"cancellation_allowed"`
	CancellationHoursBefore int       `json:"cancellation_hours_before" validate:"gte=0"`
	MaxTicketsPerPurchase   *int      `json:"max_tickets_per_purchase" validate:"omitempty,gte=1"`
}

func (a *application) createEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the authenticated organizer, set on the context by requireAuth,
	// and validated to be an organizer by requireOrganizer
	user, ok := ctx.Value(userCtx).(*store.User)
	if !ok {
		logger.Error("failed to get user from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	var payload CreateEventPayload
	if err := jsonutil.Read(w, r, &payload); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	if errs, ok := a.validator.ValidateStruct(&payload); !ok {
		jsonutil.WriteErrors(w, http.StatusUnprocessableEntity, errs)
		return
	}

	// ensure that the ends_at datetime is after starts_at
	if !payload.EndsAt.After(payload.StartsAt) {
		jsonutil.WriteError(w, http.StatusUnprocessableEntity, "ends_at must be after starts_at")
		return
	}

	// resolve omitted optional fields to their DB column defaults
	// (cancellation_allowed=true, max_tickets_per_purchase=10)
	cancellationAllowed := true
	if payload.CancellationAllowed != nil {
		cancellationAllowed = *payload.CancellationAllowed
	}
	maxTickets := 10
	if payload.MaxTicketsPerPurchase != nil {
		maxTickets = *payload.MaxTicketsPerPurchase
	}

	event := &store.Event{
		OrganizerID:             user.ID,
		Name:                    payload.Name,
		Description:             payload.Description,
		Location:                payload.Location,
		StartsAt:                payload.StartsAt,
		EndsAt:                  payload.EndsAt,
		Capacity:                payload.Capacity,
		CancellationAllowed:     cancellationAllowed,
		CancellationHoursBefore: payload.CancellationHoursBefore,
		MaxTicketsPerPurchase:   maxTickets,
	}

	if err := a.store.Events.Create(ctx, event); err != nil {
		logger.Error("failed to create event", "error", err, "user_id", user.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	type returnData struct {
		Message string       `json:"message"`
		Event   *store.Event `json:"event"`
	}
	jsonutil.WriteData(w, http.StatusCreated, returnData{
		Message: "event created successfully",
		Event:   event,
	})
}

type UpdateEventPayload struct {
	Name                    *string    `json:"name" validate:"omitempty,max=255"`
	Description             *string    `json:"description"`
	Location                *string    `json:"location" validate:"omitempty,max=255"`
	StartsAt                *time.Time `json:"starts_at"`
	EndsAt                  *time.Time `json:"ends_at"`
	Capacity                *int       `json:"capacity" validate:"omitempty,gte=1"`
	CancellationAllowed     *bool      `json:"cancellation_allowed"`
	CancellationHoursBefore *int       `json:"cancellation_hours_before" validate:"omitempty,gte=0"`
	MaxTicketsPerPurchase   *int       `json:"max_tickets_per_purchase" validate:"omitempty,gte=1"`
	ConfirmMaterialChange   *bool      `json:"confirm_material_change"`
}

func (a *application) updateEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the event, loaded by requireEventOrganizer
	event, ok := ctx.Value(eventCtx).(*store.Event)
	if !ok {
		logger.Error("failed to get event from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	var payload UpdateEventPayload
	if err := jsonutil.Read(w, r, &payload); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	if errs, ok := a.validator.ValidateStruct(&payload); !ok {
		jsonutil.WriteErrors(w, http.StatusUnprocessableEntity, errs)
		return
	}

	// snapshot material fields before merge for per-buyer notification diff
	oldEvent := *event

	// merge the non-nil fields onto the current event; material fields are
	// tracked so the published-event gate can reject them without confirmation
	changedMaterial := false

	if payload.Name != nil {
		event.Name = *payload.Name
	}
	if payload.Description != nil {
		event.Description = payload.Description
	}
	if payload.Location != nil {
		changedMaterial = true
		event.Location = *payload.Location
	}
	if payload.StartsAt != nil {
		changedMaterial = true
		event.StartsAt = *payload.StartsAt
	}
	if payload.EndsAt != nil {
		changedMaterial = true
		event.EndsAt = *payload.EndsAt
	}
	if payload.Capacity != nil {
		sold, err := a.store.Purchases.SumConfirmedQuantityByEvent(ctx, event.ID.String())
		if err != nil {
			logger.Error("failed to sum confirmed purchases", "error", err, "event_id", event.ID)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
			return
		}
		if *payload.Capacity < sold {
			jsonutil.WriteError(w, http.StatusUnprocessableEntity,
				fmt.Sprintf("capacity cannot be reduced below the %d tickets already sold", sold))
			return
		}
		event.Capacity = payload.Capacity
	}
	if payload.CancellationAllowed != nil {
		changedMaterial = true
		event.CancellationAllowed = *payload.CancellationAllowed
	}
	if payload.CancellationHoursBefore != nil {
		changedMaterial = true
		event.CancellationHoursBefore = *payload.CancellationHoursBefore
	}
	if payload.MaxTicketsPerPurchase != nil {
		event.MaxTicketsPerPurchase = *payload.MaxTicketsPerPurchase
	}

	// ends_at must stay after starts_at once the merge is applied
	if !event.EndsAt.After(event.StartsAt) {
		jsonutil.WriteError(w, http.StatusUnprocessableEntity, "ends_at must be after starts_at")
		return
	}

	switch event.Status {
	case "draft":
		// drafts have no buyers; every field is freely editable
	case "published", "sold_out":
		// material changes require explicit confirmation; without it, reject
		if changedMaterial && (payload.ConfirmMaterialChange == nil || !*payload.ConfirmMaterialChange) {
			affected, err := a.store.Purchases.SumConfirmedQuantityByEvent(ctx, event.ID.String())
			if err != nil {
				logger.Error("failed to sum confirmed purchases", "error", err, "event_id", event.ID)
				jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
				return
			}

			// zero buyers gets its own message: "affects 0 ticket holders"
			// undermines a warning that exists to protect buyers
			var msg string
			if affected > 0 {
				msg = fmt.Sprintf("this change affects %d confirmed ticket holders; pass confirm_material_change: true to proceed", affected)
			} else {
				msg = "no tickets have been sold yet; pass confirm_material_change: true to proceed"
			}
			jsonutil.WriteError(w, http.StatusConflict, msg)
			return
		}

		// a confirmed material change opens/reopens the buyers' 72h
		// cancellation grace window
		if changedMaterial {
			now := time.Now()
			event.MaterialChangedAt = &now
		}
	default: // cancelled, ended
		jsonutil.WriteError(w, http.StatusConflict, "event can no longer be updated")
		return
	}

	if err := a.store.Events.Update(ctx, event); err != nil {
		logger.Error("failed to update event", "error", err, "event_id", event.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	// send out notification emails to each buyer for confirmed material
	// changes (published/sold_out only)
	if changedMaterial && event.MaterialChangedAt != nil {
		changedFields := make(map[string]string)

		if payload.Location != nil {
			changedFields["location"] = fmt.Sprintf("%s → %s", oldEvent.Location, event.Location)
		}
		if payload.StartsAt != nil {
			changedFields["starts_at"] = fmt.Sprintf("%s → %s", oldEvent.StartsAt.UTC().Format(time.RFC3339), event.StartsAt.UTC().Format(time.RFC3339))
		}
		if payload.EndsAt != nil {
			changedFields["ends_at"] = fmt.Sprintf("%s → %s", oldEvent.EndsAt.UTC().Format(time.RFC3339), event.EndsAt.UTC().Format(time.RFC3339))
		}
		if payload.CancellationAllowed != nil {
			changedFields["cancellation_allowed"] = fmt.Sprintf("%v → %v", oldEvent.CancellationAllowed, event.CancellationAllowed)
		}
		if payload.CancellationHoursBefore != nil {
			changedFields["cancellation_hours_before"] = fmt.Sprintf("%d → %d", oldEvent.CancellationHoursBefore, event.CancellationHoursBefore)
		}
		if len(changedFields) > 0 {
			task, err := worker.NewNotifyBuyersUpdatedTask(event.ID, event.Name, changedFields, *event.MaterialChangedAt)
			if err != nil {
				logger.Error(
					fmt.Sprintf("failed to create %s task", worker.TypeNotifyBuyersUpdated),
					"error", err,
					"event_id", event.ID,
				)
			} else {
				taskInfo, err := a.worker.Enqueue(task)
				if err != nil {
					logger.Error(
						fmt.Sprintf("failed to enqueue %s task", worker.TypeNotifyBuyersUpdated),
						"error", err,
						"event_id", event.ID,
					)
				} else {
					logger.Info(
						fmt.Sprintf("enqueued %s task", worker.TypeNotifyBuyersUpdated),
						"queue", taskInfo.Queue,
						"event_id", event.ID,
						"fields", changedFields,
					)
				}
			}
		}
	}

	type returnData struct {
		Message string       `json:"message"`
		Event   *store.Event `json:"event"`
	}
	jsonutil.WriteData(w, http.StatusOK, returnData{
		Message: "event updated successfully",
		Event:   event,
	})
}

func (a *application) deleteEvent(w http.ResponseWriter, r *http.Request) {
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
		jsonutil.WriteError(w, http.StatusConflict, "event can only be deleted while in draft")
		return
	}

	if err := a.store.Events.Delete(ctx, event.ID.String()); err != nil {
		logger.Error("failed to delete event", "error", err, "event_id", event.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	type returnData struct {
		Message string `json:"message"`
	}
	jsonutil.WriteData(w, http.StatusOK, returnData{
		Message: "event deleted successfully",
	})
}

func (a *application) publishEvent(w http.ResponseWriter, r *http.Request) {
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
		jsonutil.WriteError(w, http.StatusConflict, "event can only be published while in draft")
		return
	}

	// an event needs at least one tier before it can go live
	count, err := a.store.Tiers.CountByEvent(ctx, event.ID.String())
	if err != nil {
		logger.Error("failed to count tiers", "error", err, "event_id", event.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}
	if count == 0 {
		jsonutil.WriteError(w, http.StatusConflict, "event must have at least one ticket tier before publishing")
		return
	}

	event, err = a.store.Events.Publish(ctx, event.ID.String())
	if err != nil {
		logger.Error("failed to publish event", "error", err, "event_id", event.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	type returnData struct {
		Message string       `json:"message"`
		Event   *store.Event `json:"event"`
	}
	jsonutil.WriteData(w, http.StatusOK, returnData{
		Message: "event published successfully",
		Event:   event,
	})
}

func (a *application) cancelEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the event, loaded by requireEventOrganizer
	event, ok := ctx.Value(eventCtx).(*store.Event)
	if !ok {
		logger.Error("failed to get event from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	if event.Status != "draft" && event.Status != "published" && event.Status != "sold_out" {
		jsonutil.WriteError(w, http.StatusConflict, "event can no longer be cancelled")
		return
	}

	// TODO: trigger refunds for confirmed purchases (blocked on payments
	// integration — no money moves yet, so there is nothing to refund)

	event, err := a.store.Events.Cancel(ctx, event.ID.String())
	if err != nil {
		logger.Error("failed to cancel event", "error", err, "event_id", event.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	// snapshot confirmed buyers before flipping them to 'cancelled'; the
	// worker must not re-query after the flip, or it would see zero rows
	buyers, err := a.store.Purchases.ListConfirmedBuyersByEvent(ctx, event.ID.String())
	if err != nil {
		logger.Error("failed to list confirmed buyers", "error", err, "event_id", event.ID)
		buyers = nil
	}

	// NOTE: synchronous bulk update. this is fine for low purchase volume; for 10k+
	// rows switch to a worker job or batched UPDATE with LIMIT/OFFSET + SKIP LOCKED.
	if err := a.store.Purchases.CancelByEvent(ctx, event.ID.String()); err != nil {
		logger.Error("failed to cancel purchases for event", "error", err, "event_id", event.ID)
	}
	// hard-delete waitlist entries for a dead event
	if err := a.store.Waitlist.DeleteByEvent(ctx, event.ID.String()); err != nil {
		logger.Error("failed to clear waitlist for event", "error", err, "event_id", event.ID)
	}

	// send out buyer notifications for the cancellation
	task, err := worker.NewNotifyBuyersCancelledTask(
		event.ID,
		event.Name,
		event.UpdatedAt,
		buyers,
	)
	if err != nil {
		logger.Error(
			fmt.Sprintf("failed to create %s task", worker.TypeNotifyBuyersCancelled),
			"error", err,
			"event_id", event.ID,
		)
	} else {
		taskInfo, err := a.worker.Enqueue(task)
		if err != nil {
			logger.Error(
				fmt.Sprintf("failed to enqueue %s task", worker.TypeNotifyBuyersCancelled),
				"error", err,
				"event_id", event.ID,
			)
		} else {
			logger.Info(
				fmt.Sprintf("enqueued %s task", worker.TypeNotifyBuyersCancelled),
				"queue", taskInfo.Queue,
				"event_id", event.ID,
			)
		}
	}

	type returnData struct {
		Message string       `json:"message"`
		Event   *store.Event `json:"event"`
	}
	jsonutil.WriteData(w, http.StatusOK, returnData{
		Message: "event cancelled successfully",
		Event:   event,
	})
}

// getPublicEvent loads the event in {id} for a public route and enforces strict
// visibility: draft/cancelled events are only visible to their organizer,
// everyone else gets a 404 so their existence isn't leaked. Runs behind
// maybeAuth.
func (a *application) getPublicEvent(w http.ResponseWriter, r *http.Request) *store.Event {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// ensure the {id} route param is a real UUID
	idParam := chi.URLParam(r, "id")
	if _, err := uuid.Parse(idParam); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, "invalid event id")
		return nil
	}

	event, err := a.store.Events.GetByID(ctx, idParam)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			jsonutil.WriteError(w, http.StatusNotFound, "event not found")
		default:
			logger.Error("failed to get event", "error", err, "event_id", idParam)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		}
		return nil
	}

	// unpublished events are hidden from everyone but the organizer;
	// the user was optionally resolved by maybeAuth
	user, _ := ctx.Value(userCtx).(*store.User)

	if event.Status != "published" && event.Status != "sold_out" && event.Status != "ended" {
		if user == nil || user.ID != event.OrganizerID {
			jsonutil.WriteError(w, http.StatusNotFound, "event not found")
			return nil
		}
	}

	return event
}

func (a *application) getEvent(w http.ResponseWriter, r *http.Request) {
	// runs behind maybeAuth, so a guest gets nil user from the context

	event := a.getPublicEvent(w, r)
	if event == nil {
		return // error response already written in getPublicEvent
	}

	type returnData struct {
		Message string       `json:"message"`
		Event   *store.Event `json:"event"`
	}
	jsonutil.WriteData(w, http.StatusOK, returnData{
		Message: "event retrieved successfully",
		Event:   event,
	})
}

func (a *application) getPublishedEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	limit := 50
	if lq := r.URL.Query().Get("limit"); lq != "" {
		if v, err := strconv.Atoi(lq); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}

	var cur *cursor.Cursor
	if cq := r.URL.Query().Get("cursor"); cq != "" {
		c, err := cursor.Decode(cq)
		if err != nil {
			jsonutil.WriteError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		cur = &c
	}

	// get events up to limit + 1 to ensure there's a next page
	events, err := a.store.Events.GetAllPublished(ctx, cur, limit+1)
	if err != nil {
		logger.Error("failed to get published events", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	// if there's a next page, create next cursor
	var nextCursor string
	if len(events) == limit+1 {
		events = events[:limit]
		last := events[len(events)-1]
		c := cursor.Cursor{
			Timestamp: last.StartsAt,
			ID:        last.ID,
		}
		nextCursor = cursor.Encode(c)
	}

	type returnData struct {
		Message    string         `json:"message"`
		Events     []*store.Event `json:"events"`
		NextCursor string         `json:"next_cursor"`
	}
	jsonutil.WriteData(w, http.StatusOK, returnData{
		Message:    "published events retrieved successfully",
		Events:     events,
		NextCursor: nextCursor,
	})
}
