package validator_test

import (
	"slices"
	"testing"
	"time"

	"github.com/gozsunday/gater/internal/validator"
)

type registerPayload struct {
	Name     string `json:"name" validate:"required,max=100"`
	Email    string `json:"email" validate:"required,email,max=255"`
	Password string `json:"password" validate:"required,min=8,max=96"`
}

type eventPayload struct {
	Name                    string    `json:"name" validate:"required,max=255"`
	StartsAt                time.Time `json:"starts_at" validate:"required"`
	CancellationHoursBefore int       `json:"cancellation_hours_before" validate:"gte=0"`
	MaxTicketsPerPurchase   *int      `json:"max_tickets_per_purchase" validate:"omitempty,gte=1"`
}

type tierPayload struct {
	Name     string `json:"name" validate:"required,max=255"`
	Price    *int   `json:"price" validate:"omitempty,gte=0"`
	Quantity *int   `json:"quantity" validate:"omitempty,gt=0"`
}

type tokenPayload struct {
	Token string `json:"token" validate:"required,hexadecimal,len=64"`
}

type oneofPayload struct {
	Code string `json:"code" validate:"oneof=a b"`
}

// a 64 character hex string, the shape the verification tokens have
const validHexToken = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

func TestValidateStructAcceptsValidStruct(t *testing.T) {
	v := validator.New()

	errs, ok := v.ValidateStruct(&registerPayload{
		Name:     "John",
		Email:    "john@example.com",
		Password: "supersecret",
	})
	if !ok {
		t.Errorf("validate struct: ok = %t, want true (errs: %v)", ok, errs)
	}
	if errs != nil {
		t.Errorf("validate struct: errs = %v, want nil", errs)
	}
}

func TestValidateStructReportsMissingRequiredFields(t *testing.T) {
	v := validator.New()

	errs, ok := v.ValidateStruct(&registerPayload{})
	if ok {
		t.Errorf("validate struct: ok = %t, want false", ok)
	}

	want := []string{
		"name is required",
		"email is required",
		"password is required",
	}
	if !slices.Equal(errs, want) {
		t.Errorf("validate struct: errs = %v, want %v", errs, want)
	}
}

func TestValidateStructUsesJSONTagNames(t *testing.T) {
	v := validator.New()

	// the message must name the JSON key the client sent ("starts_at"),
	// not the Go field name ("StartsAt")
	errs, ok := v.ValidateStruct(&eventPayload{Name: "Meetup"})
	if ok {
		t.Errorf("validate struct: ok = %t, want false", ok)
	}

	want := []string{"starts_at is required"}
	if !slices.Equal(errs, want) {
		t.Errorf("validate struct: errs = %v, want %v", errs, want)
	}
}

func TestValidateStructReportsInvalidEmail(t *testing.T) {
	v := validator.New()

	errs, ok := v.ValidateStruct(&registerPayload{
		Name:     "John",
		Email:    "not-an-email",
		Password: "supersecret",
	})
	if ok {
		t.Errorf("validate struct: ok = %t, want false", ok)
	}

	// the email message carries no field name
	want := []string{"invalid email format"}
	if !slices.Equal(errs, want) {
		t.Errorf("validate struct: errs = %v, want %v", errs, want)
	}
}

func TestValidateStructEnforcesLengthBounds(t *testing.T) {
	v := validator.New()

	tests := []struct {
		name     string
		password string
		want     []string
	}{
		{name: "too short", password: "1234567", want: []string{"password must have at least 8 characters"}},
		{name: "at minimum", password: "12345678", want: nil},
		{name: "at maximum", password: string(make([]byte, 96)), want: nil},
		{name: "too long", password: string(make([]byte, 97)), want: []string{"password must not have more than 96 characters"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs, ok := v.ValidateStruct(&registerPayload{
				Name:     "John",
				Email:    "john@example.com",
				Password: tt.password,
			})

			if tt.want == nil {
				if !ok {
					t.Errorf("validate struct: ok = %t, want true (errs: %v)", ok, errs)
				}
				return
			}

			if ok {
				t.Errorf("validate struct: ok = %t, want false", ok)
			}
			if !slices.Equal(errs, tt.want) {
				t.Errorf("validate struct: errs = %v, want %v", errs, tt.want)
			}
		})
	}
}

func TestValidateStructEnforcesNumericBounds(t *testing.T) {
	v := validator.New()

	tests := []struct {
		name    string
		payload tierPayload
		want    []string
	}{
		{name: "quantity below minimum", payload: tierPayload{Name: "GA", Quantity: new(0)}, want: []string{"quantity must be greater than 0"}},
		{name: "quantity at minimum", payload: tierPayload{Name: "GA", Quantity: new(1)}, want: nil},
		{name: "negative price", payload: tierPayload{Name: "GA", Price: new(-1)}, want: []string{"price must be at least 0"}},
		{name: "zero price", payload: tierPayload{Name: "GA", Price: new(0)}, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs, ok := v.ValidateStruct(&tt.payload)

			if tt.want == nil {
				if !ok {
					t.Errorf("validate struct: ok = %t, want true (errs: %v)", ok, errs)
				}
				return
			}

			if ok {
				t.Errorf("validate struct: ok = %t, want false", ok)
			}
			if !slices.Equal(errs, tt.want) {
				t.Errorf("validate struct: errs = %v, want %v", errs, tt.want)
			}
		})
	}
}

func TestValidateStructSkipsEmptyOptionalFields(t *testing.T) {
	v := validator.New()

	// omitted pointer fields are skipped entirely, even when the tag
	// would reject the zero value
	errs, ok := v.ValidateStruct(&tierPayload{Name: "GA"})
	if !ok {
		t.Errorf("validate struct: ok = %t, want true (errs: %v)", ok, errs)
	}
	if errs != nil {
		t.Errorf("validate struct: errs = %v, want nil", errs)
	}
}

func TestValidateStructReportsUnknownTagAsInvalid(t *testing.T) {
	v := validator.New()

	errs, ok := v.ValidateStruct(&oneofPayload{Code: "z"})
	if ok {
		t.Errorf("validate struct: ok = %t, want false", ok)
	}

	want := []string{"code is invalid"}
	if !slices.Equal(errs, want) {
		t.Errorf("validate struct: errs = %v, want %v", errs, want)
	}
}

func TestValidateVarAcceptsValidValue(t *testing.T) {
	v := validator.New()

	if err := v.ValidateVar("john@example.com", "email"); err != nil {
		t.Errorf("validate var: err = %v, want nil", err)
	}
}

func TestValidateVarRejectsInvalidValue(t *testing.T) {
	v := validator.New()

	err := v.ValidateVar("not-an-email", "email")
	if err == nil {
		t.Fatal("validate var: err = nil, want an error")
	}
}
