package validator

import (
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
)

type Validator interface {
	ValidateStruct(s any) ([]string, bool)
	ValidateVar(variable any, tag string) error
}

type playgroundValidator struct {
	v *validator.Validate
}

func New() Validator {
	v := validator.New(validator.WithRequiredStructEnabled())

	// report errors using the JSON tag name so messages match
	// the keys the client sent, rather than the Go field name
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name, _, _ := strings.Cut(fld.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			return fld.Name
		}
		return name
	})

	return &playgroundValidator{v: v}
}

func (v *playgroundValidator) ValidateStruct(s any) ([]string, bool) {
	err := v.v.Struct(s)
	if err == nil {
		return nil, true
	}

	errs := v.v.Struct(s).(validator.ValidationErrors)
	var errMsgs []string
	for _, e := range errs {
		field := e.Field()
		param := e.Param()

		switch e.Tag() {
		case "required":
			errMsgs = append(errMsgs, field+" is required")
		case "email":
			errMsgs = append(errMsgs, "invalid email format")
		case "gte":
			errMsgs = append(errMsgs, field+" must be at least "+param)
		case "gt":
			errMsgs = append(errMsgs, field+" must be greater than "+param)
		case "lte":
			errMsgs = append(errMsgs, field+" must not be more than "+param)
		case "lt":
			errMsgs = append(errMsgs, field+" must be less than "+param)
		case "hexadecimal":
			errMsgs = append(errMsgs, field+" must be a valid hex string")
		case "min":
			errMsgs = append(errMsgs, field+" must have at least "+param+" characters")
		case "max":
			errMsgs = append(errMsgs, field+" must not have more than "+param+" characters")
		case "len":
			errMsgs = append(errMsgs, field+" must be exactly "+param+" characters")
		default:
			errMsgs = append(errMsgs, field+" is invalid")
		}
	}

	return errMsgs, false
}

func (v *playgroundValidator) ValidateVar(variable any, tag string) error {
	return v.v.Var(variable, tag)
}
