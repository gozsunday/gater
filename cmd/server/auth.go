package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gozsunday/gater/internal/auth"
	"github.com/gozsunday/gater/internal/jsonutil"
	"github.com/gozsunday/gater/internal/store"
	"github.com/gozsunday/gater/internal/worker"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type RegisterUserPayload struct {
	Name     string `json:"name" validate:"required,max=100"`
	Email    string `json:"email" validate:"required,email,max=255"`
	Password string `json:"password" validate:"required,min=8,max=96"`
}

func (a *application) registerUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	var payload RegisterUserPayload
	if err := jsonutil.Read(w, r, &payload); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	payload.Email = strings.ToLower(payload.Email)

	if errs, ok := a.validator.ValidateStruct(&payload); !ok {
		jsonutil.WriteErrors(w, http.StatusUnprocessableEntity, errs)
		return
	}

	hash, err := auth.HashPassword(payload.Password, nil)
	if err != nil {
		logger.Error("failed to hash password", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	user := &store.User{
		Name:         payload.Name,
		Email:        payload.Email,
		PasswordHash: &hash,
		Image:        nil,
	}

	err = a.store.Users.Create(ctx, user)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrConflict):
			jsonutil.WriteError(w, http.StatusConflict, "email already registered")
		default:
			logger.Error("failed to create user", "error", err, "email", payload.Email)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		}
		return
	}

	token, err := auth.GenerateToken()
	if err != nil {
		logger.Error("failed to generate verification token", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	err = a.store.Verifications.Create(
		ctx, store.CreateVerificationParams{
			Identifier:  "email-verification:" + user.Email,
			HashedToken: token.Hash,
			ExpiresAt:   time.Now().UTC().Add(time.Hour),
		},
	)
	if err != nil {
		logger.Error("failed to create verification", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	task, err := worker.NewSendVerificationTask([]string{user.Email}, user.Name, token.Plaintext)
	if err != nil {
		logger.Error(
			fmt.Sprintf("failed to create %s task", worker.TypeSendVerificationEmail),
			"error", err,
			"username", user.Name,
		)
	} else {
		taskInfo, err := a.worker.Enqueue(task)
		if err != nil {
			logger.Error(
				fmt.Sprintf("failed to enqueue %s task", worker.TypeSendVerificationEmail),
				"error", err,
				"username", user.Name,
			)
		} else {
			logger.Info(
				fmt.Sprintf("enqueued %s task", worker.TypeSendVerificationEmail),
				"queue", taskInfo.Queue,
				"username", user.Name,
			)
		}
	}

	type returnData struct {
		Message string      `json:"message"`
		User    *store.User `json:"user"`
	}
	jsonutil.WriteData(w, http.StatusCreated, returnData{
		Message: "account created successfully",
		User:    user,
	})
}

type LoginUserPayload struct {
	Email    string `json:"email" validate:"required,email,max=255"`
	Password string `json:"password" validate:"required,min=8,max=96"`
}

func (a *application) loginUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	var payload LoginUserPayload
	if err := jsonutil.Read(w, r, &payload); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	payload.Email = strings.ToLower(payload.Email)

	if errs, ok := a.validator.ValidateStruct(&payload); !ok {
		jsonutil.WriteErrors(w, http.StatusUnprocessableEntity, errs)
		return
	}

	user, err := a.store.Users.GetByEmail(ctx, payload.Email)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			jsonutil.WriteError(w, http.StatusUnauthorized, "invalid email or password")
		default:
			logger.Error("failed to get user by email", "error", err, "email", payload.Email)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		}
		return
	}

	if user.PasswordHash == nil {
		jsonutil.WriteError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	matched, err := auth.VerifyPassword(payload.Password, *user.PasswordHash)
	if err != nil {
		logger.Error("failed to verify password", "error", err, "email", payload.Email)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}
	if !matched {
		jsonutil.WriteError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	ua := r.UserAgent()
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		logger.Error("failed to get IP address", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	var token *auth.Token
	var sessionCreated bool

	// try to create session token and user session, retry twice if a collision occurs
	for range 3 {
		token, err = auth.GenerateToken()
		if err != nil {
			logger.Error("failed to generate token", "error", err)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
			return
		}

		session := &store.Session{
			UserID:    user.ID,
			TokenHash: token.Hash,
			IPAddress: &ip,
			UserAgent: &ua,
			ExpiresAt: time.Now().UTC().Add(time.Hour * 24 * 30),
		}

		err = a.store.Sessions.Create(ctx, session)
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		if err != nil {
			logger.Error("failed to create session", "error", err, "user_id", user.ID)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
			return
		}

		sessionCreated = true
		break
	}

	if !sessionCreated {
		logger.Error("failed to create session")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	type returnData struct {
		Message string      `json:"message"`
		Token   string      `json:"token"`
		User    *store.User `json:"user"`
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "gater_auth_session",
		Value:    token.Plaintext,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.config.IsProduction(), // only over HTTPS in prod
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60 * 60 * 24 * 30, // 30 days to expiry
	})
	jsonutil.WriteData(w, http.StatusOK, returnData{
		Message: "logged in successfully",
		Token:   token.Plaintext,
		User:    user,
	})
}

func (a *application) logoutUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// the session of the user, set on the context by requireAuth
	session, ok := ctx.Value(sessionCtx).(*store.Session)
	if !ok {
		logger.Error("failed to get session from context")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	err := a.store.Sessions.Delete(ctx, session.ID)
	if err != nil {
		logger.Error("failed to delete session", "error", err, "session_id", session.ID)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	type returnData struct {
		Message string `json:"message"`
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "gater_auth_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.config.IsProduction(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1, // to delete the cookie
	})
	jsonutil.WriteData(w, http.StatusOK, returnData{Message: "logged out successfully"})
}

type VerifyEmailPayload struct {
	Token string `json:"token" validate:"required,hexadecimal,len=64"`
}

func (a *application) verifyEmail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	var payload VerifyEmailPayload
	if err := jsonutil.Read(w, r, &payload); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	if errs, ok := a.validator.ValidateStruct(&payload); !ok {
		jsonutil.WriteErrors(w, http.StatusUnprocessableEntity, errs)
		return
	}

	hashedToken := auth.HashToken(payload.Token)

	verification, err := a.store.Verifications.Get(ctx, hashedToken)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			jsonutil.WriteError(w, http.StatusBadRequest, "invalid or expired token")
		default:
			logger.Error("failed to get verification", "error", err)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		}
		return
	}

	// check if token has expired
	if i := time.Now().UTC().Compare(verification.ExpiresAt); i >= 0 {
		// delete token
		if err := a.store.Verifications.Delete(ctx, verification.ID); err != nil {
			logger.Error("failed to delete verification", "error", err)
		}
		jsonutil.WriteError(w, http.StatusBadRequest, "invalid or expired token")
		return
	}

	email, ok := strings.CutPrefix(verification.Identifier, "email-verification:")
	if !ok {
		logger.Error("unexpected verification identifier", "identifier", verification.Identifier)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	// mark user as verified
	if err := a.store.Users.MarkVerified(ctx, email); err != nil {
		logger.Error("failed to mark user as verified", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	// delete token
	if err := a.store.Verifications.Delete(ctx, verification.ID); err != nil {
		logger.Error("failed to delete verification", "error", err)
	}

	type returnData struct {
		Message string `json:"message"`
	}
	jsonutil.WriteData(w, http.StatusOK, returnData{
		Message: "email verified successfully",
	})
}

type ResendVerificationPayload struct {
	Email string `json:"email" validate:"required,email,max=255"`
}

func (a *application) resendVerificationEmail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	var payload ResendVerificationPayload
	if err := jsonutil.Read(w, r, &payload); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	payload.Email = strings.ToLower(payload.Email)

	if errs, ok := a.validator.ValidateStruct(&payload); !ok {
		jsonutil.WriteErrors(w, http.StatusUnprocessableEntity, errs)
		return
	}

	type returnData struct {
		Message string `json:"message"`
	}
	const successMsg = "if the account with this email exists and is unverified, a verification email has been sent"

	user, err := a.store.Users.GetByEmail(ctx, payload.Email)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			jsonutil.WriteData(w, http.StatusOK, returnData{Message: successMsg})
		default:
			logger.Error("failed to get user by email", "error", err)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		}
		return
	}

	if user.EmailVerified {
		jsonutil.WriteData(w, http.StatusOK, returnData{Message: successMsg})
		return
	}

	identifier := "email-verification:" + user.Email

	latestVerification, err := a.store.Verifications.GetLatest(ctx, identifier)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		logger.Error("failed to get latest verifications", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	verificationsCount, err := a.store.Verifications.CountSince(ctx, identifier, time.Hour)
	if err != nil {
		logger.Error("failed to get verifications count", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	// only allow resend if there are less than 5 verification tokens
	// created in an hour and none created in the last 1 minute
	allowResend := latestVerification == nil || (verificationsCount < 5 && time.Now().UTC().Add(-time.Minute).Compare(latestVerification.CreatedAt) == 1)

	if !allowResend {
		jsonutil.WriteData(w, http.StatusOK, returnData{Message: successMsg})
		return
	}

	err = a.store.Verifications.DeleteByIdentifier(ctx, identifier)
	if err != nil {
		logger.Error("failed to delete verification(s)", "error", err)
	}

	token, err := auth.GenerateToken()
	if err != nil {
		logger.Error("failed to generate verification token", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	err = a.store.Verifications.Create(
		ctx, store.CreateVerificationParams{
			Identifier:  identifier,
			HashedToken: token.Hash,
			ExpiresAt:   time.Now().UTC().Add(time.Hour),
		},
	)
	if err != nil {
		logger.Error("failed to create verification", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	sendVerificationTask, err := worker.NewSendVerificationTask(
		[]string{user.Email},
		user.Name, token.Plaintext,
	)
	if err != nil {
		logger.Error(
			fmt.Sprintf("failed to create %s task", worker.TypeSendVerificationEmail),
			"error", err,
			"username", user.Name,
		)
	} else {
		taskInfo, err := a.worker.Enqueue(sendVerificationTask)
		if err != nil {
			logger.Error(
				fmt.Sprintf("failed to enqueue %s task", worker.TypeSendVerificationEmail),
				"error", err,
				"username", user.Name,
			)
		} else {
			logger.Info(
				fmt.Sprintf("enqueued %s task", worker.TypeSendVerificationEmail),
				"queue", taskInfo.Queue,
				"username", user.Name,
			)
		}
	}

	jsonutil.WriteData(w, http.StatusOK, returnData{Message: successMsg})
}

type ForgotPasswordPayload struct {
	Email string `json:"email" validate:"required,email,max=255"`
}

func (a *application) forgotPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	var payload ForgotPasswordPayload
	if err := jsonutil.Read(w, r, &payload); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	payload.Email = strings.ToLower(payload.Email)

	if errs, ok := a.validator.ValidateStruct(&payload); !ok {
		jsonutil.WriteErrors(w, http.StatusUnprocessableEntity, errs)
		return
	}

	type returnData struct {
		Message string `json:"message"`
	}
	const successMsg = "if the account with this email exists, a password reset email has been sent"

	user, err := a.store.Users.GetByEmail(ctx, payload.Email)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			jsonutil.WriteData(w, http.StatusOK, returnData{Message: successMsg})
		default:
			logger.Error("failed to get user by email", "error", err)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		}
		return
	}

	identifier := "password-reset:" + user.Email

	latestPasswordReset, err := a.store.Verifications.GetLatest(ctx, identifier)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		logger.Error("failed to get latest password resets", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	passwordResetsCount, err := a.store.Verifications.CountSince(ctx, identifier, time.Hour)
	if err != nil {
		logger.Error("failed to get password resets count", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	// only allow send if there are less than 5 password reset tokens
	// created in an hour and none created in the last 1 minute
	allowSend := latestPasswordReset == nil || (passwordResetsCount < 5 && time.Now().UTC().Add(-time.Minute).Compare(latestPasswordReset.CreatedAt) == 1)

	if !allowSend {
		jsonutil.WriteData(w, http.StatusOK, returnData{Message: successMsg})
		return
	}

	err = a.store.Verifications.DeleteByIdentifier(ctx, identifier)
	if err != nil {
		logger.Error("failed to delete password reset(s)", "error", err)
	}

	token, err := auth.GenerateToken()
	if err != nil {
		logger.Error("failed to generate password reset token", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	err = a.store.Verifications.Create(
		ctx, store.CreateVerificationParams{
			Identifier:  identifier,
			HashedToken: token.Hash,
			ExpiresAt:   time.Now().UTC().Add(15 * time.Minute),
		},
	)
	if err != nil {
		logger.Error("failed to create password reset", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	sendPwdResetTask, err := worker.NewSendPwdResetTask(
		[]string{user.Email},
		user.Name,
		token.Plaintext,
	)
	if err != nil {
		logger.Error(
			fmt.Sprintf("failed to create %s task", worker.TypeSendPasswordResetEmail),
			"error", err,
			"username", user.Name,
		)
	} else {
		taskInfo, err := a.worker.Enqueue(sendPwdResetTask)
		if err != nil {
			logger.Error(
				fmt.Sprintf("failed to enqueue %s task", worker.TypeSendPasswordResetEmail),
				"error", err,
				"username", user.Name,
			)
		} else {
			logger.Info(
				fmt.Sprintf("enqueued %s task", worker.TypeSendPasswordResetEmail),
				"queue", taskInfo.Queue,
				"username", user.Name,
			)
		}
	}

	jsonutil.WriteData(w, http.StatusOK, returnData{Message: successMsg})
}

type ResetPasswordPayload struct {
	Token    string `json:"token" validate:"required,hexadecimal,len=64"`
	Password string `json:"password" validate:"required,min=8,max=96"`
}

func (a *application) resetPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	var payload ResetPasswordPayload
	if err := jsonutil.Read(w, r, &payload); err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	if errs, ok := a.validator.ValidateStruct(&payload); !ok {
		jsonutil.WriteErrors(w, http.StatusUnprocessableEntity, errs)
		return
	}

	hashedToken := auth.HashToken(payload.Token)

	passwordReset, err := a.store.Verifications.Get(ctx, hashedToken)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			jsonutil.WriteError(w, http.StatusBadRequest, "invalid or expired token")
		default:
			logger.Error("failed to get password reset", "error", err)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		}
		return
	}

	// check if token has expired
	if i := time.Now().UTC().Compare(passwordReset.ExpiresAt); i >= 0 {
		// delete token
		if err := a.store.Verifications.Delete(ctx, passwordReset.ID); err != nil {
			logger.Error("failed to delete password reset", "error", err)
		}
		jsonutil.WriteError(w, http.StatusBadRequest, "invalid or expired token")
		return
	}

	hashedPassword, err := auth.HashPassword(payload.Password, nil)
	if err != nil {
		logger.Error("failed to hash password", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	email, ok := strings.CutPrefix(passwordReset.Identifier, "password-reset:")
	if !ok {
		logger.Error("unexpected password reset identifier", "identifier", passwordReset.Identifier)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	user, err := a.store.Users.GetByEmail(ctx, email)
	if err != nil {
		logger.Error("failed to get user by email", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	if err := a.store.Users.ResetPassword(ctx, user.Email, hashedPassword); err != nil {
		logger.Error("failed to reset password", "error", err, "email", user.Email)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	if err := a.store.Verifications.Delete(ctx, passwordReset.ID); err != nil {
		logger.Error("failed to delete password reset", "error", err)
	}

	// delete all user's sessions
	if err := a.store.Sessions.DeleteAll(ctx, user.ID); err != nil {
		logger.Error("failed to delete user's sessions", "error", err)
	}

	type returnData struct {
		Message string `json:"message"`
	}
	jsonutil.WriteData(w, http.StatusOK, returnData{
		Message: "password reset successfully",
	})
}

func (a *application) googleOAuthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     a.config.GoogleClientID,
		ClientSecret: a.config.GoogleClientSecret,
		RedirectURL:  a.config.GoogleRedirectURI,
		Scopes:       []string{"email", "profile"},
		Endpoint:     google.Endpoint,
	}
}

func (a *application) google(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	// build oauth2 config
	oauth2Config := a.googleOAuthConfig()

	state, err := auth.GenerateRandomOAuthState()
	if err != nil {
		logger.Error("failed to generate random oauth state", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "gater_oauth_state",
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.config.IsProduction(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60 * 10, // 10 mins to expiry
	})

	url := oauth2Config.AuthCodeURL(state)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

func (a *application) googleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := loggerFromCtx(ctx)

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	stateCookie, err := r.Cookie("gater_oauth_state")
	if err != nil {
		jsonutil.WriteError(w, http.StatusBadRequest, "missing or invalid state")
		return
	}

	// if state != stateCookie.Value {
	if subtle.ConstantTimeCompare([]byte(state), []byte(stateCookie.Value)) == 0 {
		logger.Error("oauth state mismatch", "state", state)
		jsonutil.WriteError(w, http.StatusBadRequest, "invalid state")
		return
	}

	// clear the state cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "gater_oauth_state",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.config.IsProduction(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1, // to delete the cookie
	})

	// build oauth2 config
	oauth2Config := a.googleOAuthConfig()

	oauth2Token, err := oauth2Config.Exchange(ctx, code)
	if err != nil {
		logger.Error("failed to convert oauth code into token", "error", err, "state", state)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	client := oauth2Config.Client(ctx, oauth2Token)
	resp, err := client.Get("https://openidconnect.googleapis.com/v1/userinfo")
	if err != nil {
		logger.Error("failed to get user", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Error("failed to get user", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	var googleUser struct {
		ID      string `json:"id"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&googleUser); err != nil {
		logger.Error("failed to get user", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}
	googleUser.Email = strings.ToLower(googleUser.Email)

	user := &store.User{}

	// check if oauth account already exists
	oauthAccount, err := a.store.OAuthAccounts.GetByProviderAndAccountID(
		ctx,
		"google",
		googleUser.ID,
	)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		logger.Error("failed to get oauth account", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	if oauthAccount != nil {
		// if oauth account exists, get user from it
		user, err = a.store.Users.GetByID(ctx, oauthAccount.UserID.String())
		if err != nil {
			logger.Error("failed to get user", "error", err)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
			return
		}
	} else {
		// if there's no oauth account, get user from email
		user, err = a.store.Users.GetByEmail(ctx, googleUser.Email)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			logger.Error("failed to get user", "error", err)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
			return
		}

		// if the user hasnt been created yet (new user), create a user
		if user == nil {
			user = &store.User{
				Name:          googleUser.Name,
				Email:         googleUser.Email,
				Image:         &googleUser.Picture,
				EmailVerified: true,
			}

			err := a.store.Users.Create(ctx, user)
			if err != nil {
				logger.Error("failed to create user", "error", err, "email", googleUser.Email)
				jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
				return
			}
		} else if !user.EmailVerified {
			// if the user has been created but isn't verified, verify them
			if err := a.store.Users.MarkVerified(ctx, user.Email); err != nil {
				logger.Error("failed to verify already existing user", "error", err, "email", user.Email)
			}
		}

		// create oauth account linked to user
		scope, _ := oauth2Token.Extra("scope").(string)
		oa := &store.OAuthAccount{
			UserID:            user.ID,
			Provider:          "google",
			ProviderAccountID: googleUser.ID,
			Scope:             scope,
		}

		err = a.store.OAuthAccounts.Create(ctx, oa)
		if err != nil {
			logger.Error("failed to create oauth account", "error", err, "google_user_id", googleUser.ID)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
			return
		}
	}

	ua := r.UserAgent()
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		logger.Error("failed to get IP address", "error", err)
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	var token *auth.Token
	var sessionCreated bool

	// try to create session token and user session, retry twice if a collision occurs
	for range 3 {
		token, err = auth.GenerateToken()
		if err != nil {
			logger.Error("failed to generate token", "error", err)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
			return
		}

		session := &store.Session{
			UserID:    user.ID,
			TokenHash: token.Hash,
			IPAddress: &ip,
			UserAgent: &ua,
			ExpiresAt: time.Now().UTC().Add(time.Hour * 24 * 30),
		}

		err = a.store.Sessions.Create(ctx, session)
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		if err != nil {
			logger.Error("failed to create session", "error", err, "user_id", user.ID)
			jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
			return
		}

		sessionCreated = true
		break
	}

	if !sessionCreated {
		logger.Error("failed to create session")
		jsonutil.WriteError(w, http.StatusInternalServerError, "something went wrong")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "gater_auth_session",
		Value:    token.Plaintext,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.config.IsProduction(), // only over HTTPS in prod
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60 * 60 * 24 * 30, // 30 days to expiry
	})

	http.Redirect(w, r, a.config.FrontendURL, http.StatusFound)
}
