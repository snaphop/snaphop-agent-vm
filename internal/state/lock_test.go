package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLockVM_SerializesOperationsOnTheSameVM(t *testing.T) {
	store := newStore(t)

	held, err := store.TryLockVM("agent-01", "create")
	if err != nil {
		t.Fatalf("TryLockVM: %v", err)
	}
	t.Cleanup(func() { _ = held.Release() })

	// A second lock within this process must also be refused: two goroutines
	// racing a create is the same bug as two processes racing it.
	_, err = store.TryLockVM("agent-01", "destroy")
	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("second TryLockVM = %v, want *BusyError", err)
	}
	if busy.Holder == "" {
		t.Error("BusyError does not name the holder, so the operator cannot tell what is running")
	}
}

func TestLockVM_DifferentVMsDoNotBlockEachOther(t *testing.T) {
	store := newStore(t)

	first, err := store.TryLockVM("agent-01", "create")
	if err != nil {
		t.Fatalf("locking agent-01: %v", err)
	}
	defer func() { _ = first.Release() }()

	second, err := store.TryLockVM("agent-02", "create")
	if err != nil {
		t.Fatalf("locking agent-02 while agent-01 is locked: %v", err)
	}
	defer func() { _ = second.Release() }()
}

func TestLockVM_ReleaseAllowsTheNextHolder(t *testing.T) {
	store := newStore(t)

	first, err := store.TryLockVM("agent-01", "create")
	if err != nil {
		t.Fatalf("TryLockVM: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	second, err := store.TryLockVM("agent-01", "destroy")
	if err != nil {
		t.Fatalf("TryLockVM after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// Releasing twice is a no-op, so a deferred Release after an explicit one
	// is safe.
	if err := second.Release(); err != nil {
		t.Errorf("second Release: %v", err)
	}
}

func TestLockVM_WaitsUntilTheContextExpires(t *testing.T) {
	store := newStore(t)

	held, err := store.TryLockVM("agent-01", "create")
	if err != nil {
		t.Fatalf("TryLockVM: %v", err)
	}
	defer func() { _ = held.Release() }()

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = store.LockVM(ctx, "agent-01", "destroy")
	if err == nil {
		t.Fatal("LockVM acquired a lock another holder has")
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Errorf("LockVM gave up after %s, want it to wait for the context deadline", elapsed)
	}
}

func TestLockImage_SerializesBuildsOfTheSameImage(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	held, err := store.LockImage(ctx, "ubuntu", "24.04", "image build")
	if err != nil {
		t.Fatalf("LockImage: %v", err)
	}
	defer func() { _ = held.Release() }()

	// Different distros build concurrently; the same one does not.
	other, err := store.LockImage(ctx, "fedora", "42", "image build")
	if err != nil {
		t.Fatalf("LockImage for a different distro: %v", err)
	}
	defer func() { _ = other.Release() }()

	waitCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := store.LockImage(waitCtx, "ubuntu", "24.04", "image build"); err == nil {
		t.Error("a second build of the same base image was allowed to start")
	}
}
