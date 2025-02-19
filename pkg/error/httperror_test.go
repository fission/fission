package error

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsNotFound(t *testing.T) {
	errs := map[error]bool{
		nil: false,
		MakeError(ErrorNotFound, "someone not found"):                                             true,
		MakeError(ErrorTooManyRequests, "too many requests"):                                      false,
		fmt.Errorf("other information: %w", MakeError(ErrorNotFound, "someone not found")):        true,
		fmt.Errorf("other information: %w", MakeError(ErrorTooManyRequests, "too many requests")): false,
	}

	for err, want := range errs {
		assert.Equal(t, want, IsNotFound(err))
	}
}

func TestGetHTTPError(t *testing.T) {
	errs := map[int]error{
		http.StatusBadRequest:      MakeError(ErrorInvalidArgument, ""),
		http.StatusConflict:        fmt.Errorf("%w", MakeError(ErrorNameExists, "")),
		http.StatusNotFound:        fmt.Errorf("%w", MakeError(ErrorNotFound, "")),
		http.StatusTooManyRequests: fmt.Errorf("other information: %w", MakeError(ErrorTooManyRequests, "too many requests")),
	}
	for want, err := range errs {
		code, _ := GetHTTPError(err)
		assert.Equal(t, want, code)
	}
}
