package error

import (
	"net/http"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
)

func TestIsNotFound(t *testing.T) {
	errs := map[error]bool{
		nil: false,
		MakeError(ErrorNotFound, "someone not found"):                                          true,
		MakeError(ErrorTooManyRequests, "too many requests"):                                   false,
		errors.Wrap(MakeError(ErrorNotFound, "someone not found"), "other information"):        true,
		errors.Wrap(MakeError(ErrorTooManyRequests, "too many requests"), "other information"): false,
	}

	for err, want := range errs {
		assert.Equal(t, want, IsNotFound(err))
	}
}

func TestGetHTTPError(t *testing.T) {
	errs := map[int]error{
		http.StatusBadRequest:      MakeError(ErrorInvalidArgument, ""),
		http.StatusConflict:        errors.Wrap(MakeError(ErrorNameExists, ""), ""),
		http.StatusNotFound:        errors.Wrap(MakeError(ErrorNotFound, ""), ""),
		http.StatusTooManyRequests: errors.Wrap(MakeError(ErrorTooManyRequests, "too many requests"), "other information"),
	}
	for want, err := range errs {
		code, _ := GetHTTPError(err)
		assert.Equal(t, want, code)
	}
}
