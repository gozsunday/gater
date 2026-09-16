package main

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gozsunday/gater/internal/jsonutil"
	"github.com/gozsunday/gater/internal/qr"
	"github.com/gozsunday/gater/internal/store"
)

type CheckInPayload struct {
	Token string `json:"token" validate:"required"`
}

func (a *application) checkIn(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the event, loaded by requireEventOrganizer
	event, ok := ctx.Value(eventCtx).(*store.Event)
	if !ok {
		logger.Error("failed to get event from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	var payload CheckInPayload
	if err := jsonutil.Read(w, r, &payload); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	if errs, ok := a.validator.ValidateStruct(&payload); !ok {
		jsonutil.WriteErrors(w, http.StatusUnprocessableEntity, errs)
		return
	}

	type returnError struct {
		Valid  bool   `json:"valid"`
		Reason string `json:"reason"`
	}

	ticketID, err := qr.VerifyToken(payload.Token, a.config.TicketSecret)
	if err != nil {
		jsonutil.WriteData(w, http.StatusOK, returnError{
			Valid:  false,
			Reason: "invalid token",
		})
		return
	}

	ticketCheckIn, err := a.store.Tickets.CheckIn(ctx, ticketID, event.ID)
	if err != nil {
		errReason := ""

		switch {
		case errors.Is(err, store.ErrNotFound):
			errReason = "invalid token"
		case errors.Is(err, store.ErrAlreadyCheckedIn):
			errReason = "already checked in"
		case errors.Is(err, store.ErrTicketCancelled):
			errReason = "ticket cancelled"
		case errors.Is(err, store.ErrWrongEvent):
			errReason = "wrong event"
		}

		if errReason == "" {
			logger.Error(
				"failed to complete ticket check in",
				"error", err, "ticket_id", ticketID, "event_id", event.ID,
			)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		} else {
			jsonutil.WriteData(w, http.StatusOK, returnError{
				Valid:  false,
				Reason: errReason,
			})
		}
		return
	}

	type attendee struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	type ticket struct {
		ID          uuid.UUID  `json:"id"`
		Tier        string     `json:"tier"`
		CheckedInAt *time.Time `json:"checked_in_at"`
	}
	type returnData struct {
		Valid    bool     `json:"valid"`
		Attendee attendee `json:"attendee"`
		Ticket   ticket   `json:"ticket"`
	}

	jsonutil.WriteData(w, http.StatusOK, returnData{
		Valid: true,
		Attendee: attendee{
			Name:  ticketCheckIn.AttendeeName,
			Email: ticketCheckIn.AttendeeEmail,
		},
		Ticket: ticket{
			ID:          ticketCheckIn.Ticket.ID,
			Tier:        ticketCheckIn.TierName,
			CheckedInAt: ticketCheckIn.Ticket.CheckedInAt,
		},
	})
}
