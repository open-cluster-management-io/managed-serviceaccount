package provisioner

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	managerprovisioner "open-cluster-management.io/managed-serviceaccount/pkg/addon/manager/provisioner"
)

func TestRunReconcileCleanupInvokesCleanupAndReturns(t *testing.T) {
	opts := validOptions()
	opts.Cleanup = true
	stub := &stubReconciler{}

	err := opts.runReconcile(context.Background(), stub)

	assert.NoError(t, err)
	assert.Equal(t, int64(1), atomic.LoadInt64(&stub.cleanupCalls))
	assert.Equal(t, int64(0), atomic.LoadInt64(&stub.syncCalls))
}

func TestRunReconcileCleanupPropagatesError(t *testing.T) {
	opts := validOptions()
	opts.Cleanup = true
	stub := &stubReconciler{cleanupErr: errors.New("cleanup failed")}

	err := opts.runReconcile(context.Background(), stub)

	assert.EqualError(t, err, "cleanup failed")
}

func TestRunReconcileLoopSyncsUntilContextCancelled(t *testing.T) {
	opts := validOptions()
	opts.SyncInterval = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	stub := &stubReconciler{
		onSync: func(count int64) error {
			if count >= 3 {
				cancel()
			}
			return nil
		},
	}

	err := opts.runReconcile(ctx, stub)

	assert.ErrorIs(t, err, context.Canceled)
	assert.GreaterOrEqual(t, atomic.LoadInt64(&stub.syncCalls), int64(3))
	assert.Equal(t, int64(0), atomic.LoadInt64(&stub.cleanupCalls))
}

func TestRunReconcileLoopContinuesAfterSyncError(t *testing.T) {
	opts := validOptions()
	opts.SyncInterval = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	stub := &stubReconciler{
		onSync: func(count int64) error {
			if count >= 2 {
				cancel()
				return nil
			}
			return fmt.Errorf("transient sync error %d", count)
		},
	}

	err := opts.runReconcile(ctx, stub)

	assert.ErrorIs(t, err, context.Canceled)
	assert.GreaterOrEqual(t, atomic.LoadInt64(&stub.syncCalls), int64(2))
}

func TestRunReconcileLoopRetriesQuicklyAfterTransientError(t *testing.T) {
	// a transient failure must retry on the error backoff, not the full --sync-interval
	opts := validOptions()
	opts.SyncInterval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	stub := &stubReconciler{
		onSync: func(count int64) error {
			if count == 1 {
				return errors.New("transient")
			}
			cancel()
			return nil
		},
	}

	start := time.Now()
	err := opts.runReconcile(ctx, stub)
	elapsed := time.Since(start)

	assert.ErrorIs(t, err, context.Canceled)
	assert.GreaterOrEqual(t, atomic.LoadInt64(&stub.syncCalls), int64(2))
	assert.Less(t, elapsed, 30*time.Second)
}

func TestRunReconcileLoopReturnsImmediatelyWhenContextAlreadyCancelled(t *testing.T) {
	opts := validOptions()
	opts.SyncInterval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stub := &stubReconciler{}

	done := make(chan error, 1)
	go func() {
		done <- opts.runReconcile(ctx, stub)
	}()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("runReconcile did not return after context cancellation")
	}

	// Sync runs once before the cancellation check; Cleanup must not be invoked in sync mode.
	assert.Equal(t, int64(1), atomic.LoadInt64(&stub.syncCalls))
	assert.Equal(t, int64(0), atomic.LoadInt64(&stub.cleanupCalls))
}

func validOptions() *ManagedKubeconfigProvisionerOptions {
	return &ManagedKubeconfigProvisionerOptions{
		ClusterName:                    "cluster-1",
		SourceNamespace:                "source-ns",
		SourceSecret:                   managerprovisioner.DefaultExternalManagedKubeConfigSecret,
		TargetNamespace:                "addon-ns",
		TargetSecret:                   "target-kubeconfig",
		ManagedServiceAccountNamespace: "addon-ns",
		ManagedServiceAccountName:      managerprovisioner.DefaultManagedServiceAccountName,
		TokenExpirationSeconds:         managerprovisioner.DefaultTokenExpirationSeconds,
		RefreshBefore:                  managerprovisioner.DefaultRefreshBefore,
		SyncInterval:                   5 * time.Minute,
	}
}

type stubReconciler struct {
	syncCalls    int64
	cleanupCalls int64
	cleanupErr   error
	onSync       func(count int64) error
}

func (s *stubReconciler) Sync(ctx context.Context) error {
	count := atomic.AddInt64(&s.syncCalls, 1)
	if s.onSync == nil {
		return nil
	}
	return s.onSync(count)
}

func (s *stubReconciler) Cleanup(ctx context.Context) error {
	atomic.AddInt64(&s.cleanupCalls, 1)
	return s.cleanupErr
}
