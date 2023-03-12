package error

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsNotFound(t *testing.T) {
	assert.False(t, IsNotFound(nil))
}
