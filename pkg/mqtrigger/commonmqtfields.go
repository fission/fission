package mqtrigger

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// FissionMetadata contains common fission side fields
type FissionMetadata struct {
	// fission
	Topic         string
	ResponseTopic string
	ErrorTopic    string
	FunctionURL   string
	MaxRetries    int
	ContentType   string
	TriggerName   string
}

// ParseFissionMetadata parses fission side common fields and returns as fissionMetadata or returns error
func ParseFissionMetadata() (FissionMetadata, error) {
	for _, envVars := range []string{"TOPIC", "RESPONSE_TOPIC", "ERROR_TOPIC", "FUNCTION_URL", "MAX_RETRIES", "CONTENT_TYPE", "TRIGGER_NAME"} {
		if os.Getenv(envVars) == "" {
			return FissionMetadata{}, fmt.Errorf("Environment variable not found: %v", envVars)
		}
	}
	meta := FissionMetadata{
		Topic:         os.Getenv("TOPIC"),
		ResponseTopic: os.Getenv("RESPONSE_TOPIC"),
		ErrorTopic:    os.Getenv("ERROR_TOPIC"),
		FunctionURL:   os.Getenv("FUNCTION_URL"),
		ContentType:   os.Getenv("CONTENT_TYPE"),
		TriggerName:   os.Getenv("TRIGGER_NAME"),
	}
	val, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("MAX_RETRIES")), 0, 64)
	if err != nil {
		return FissionMetadata{}, fmt.Errorf("Failed to parse value from MAX_RETRIES environment variable %v", err)
	}
	meta.MaxRetries = int(val)
	return meta, nil
}
