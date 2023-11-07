package manager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAddAndWait(t *testing.T) {
	mgr := New()

	value := 10
	expectedValue := 11

	mgr.Add(context.Background(), func(ctx context.Context) {
		time.Sleep(1 * time.Second)
		value = expectedValue
	})

	mgr.Wait()
	require.Equal(t, expectedValue, value, "manager did not wait for go routine to complete")
}

func TestAddWithContextCancel(t *testing.T) {
	mgr := New()

	value := 10
	expectedValue := 11

	ctx, cancel := context.WithCancel(context.Background())

	mgr.Add(ctx, func(ctx context.Context) {
		<-ctx.Done()
		value = 11
	})

	go cancel()
	mgr.Wait()
	require.Equal(t, expectedValue, value, "manager did not wait for go routine to complete when context is cancelled")
}

func TestAddAndWaitWithTimeout(t *testing.T) {
	mgr := New()

	value := 10

	mgr.Add(context.Background(), func(ctx context.Context) {
		time.Sleep(2 * time.Second)
	})

	err := mgr.WaitWithTimeout(1 * time.Second)
	require.NotNil(t, err, "manager WaitWithTimeout did not return an error when timeout exceeded")

	expectedValue := 11
	mgr.Add(context.Background(), func(ctx context.Context) {
		time.Sleep(100 * time.Millisecond)
		value = expectedValue
	})

	err = mgr.WaitWithTimeout(1 * time.Second)
	require.Nil(t, err, "manager returned error even though all go routins completed successfully before timeout")
	require.Equal(t, expectedValue, value)
}
