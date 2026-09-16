package main

import (
	"net/http"

	"github.com/gozsunday/gater/internal/jsonutil"
	"github.com/gozsunday/gater/internal/store"
)

func (a *application) getUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the authenticated user, set on the context by requireAuth
	user, ok := ctx.Value(userCtx).(*store.User)
	if !ok {
		logger.Error("failed to get user from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	type returnData struct {
		User *store.User `json:"user"`
	}
	jsonutil.WriteData(w, http.StatusOK, returnData{User: user})
}

func (a *application) becomeOrganizer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the authenticated user, set on the context by requireAuth
	user, ok := ctx.Value(userCtx).(*store.User)
	if !ok {
		logger.Error("failed to get user from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	type returnData struct {
		Message string      `json:"message"`
		User    *store.User `json:"user"`
	}

	if user.Role == "organizer" {
		jsonutil.WriteData(w, http.StatusOK, returnData{
			Message: "already an organizer",
			User:    user,
		})
		return
	}

	user, err := a.store.Users.BecomeOrganizer(ctx, user.ID.String())
	if err != nil {
		logger.Error("failed to set user as organizer", "error", err, "user_id", user.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	jsonutil.WriteData(w, http.StatusOK, returnData{
		Message: "organizer role activated",
		User:    user,
	})
}
