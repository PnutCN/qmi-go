package manager

import (
	"context"
	"testing"
)

func TestLeaseServicesFailsWithoutOpenClient(t *testing.T) {
	m := newRecoveryTestManager()
	m.client = nil

	leased, err := m.LeaseServices(context.Background())
	if err == nil {
		t.Fatal("expected an error when the Manager has no open QMI client")
	}
	if leased != nil {
		t.Fatalf("leased = %+v, want nil on error", leased)
	}
}

func TestLeasedServicesCloseIsNilSafe(t *testing.T) {
	var l *LeasedServices
	if err := l.Close(); err != nil {
		t.Fatalf("Close() on nil *LeasedServices = %v, want nil", err)
	}
}

func TestLeasedServicesClosePartialIsSafe(t *testing.T) {
	// A lease that failed after allocating only WDA must be safe to Close
	// with the other fields left nil (this is exactly the state
	// LeaseServices leaves behind on its own WDS/NAS allocation failure
	// paths before returning the error).
	l := &LeasedServices{}
	if err := l.Close(); err != nil {
		t.Fatalf("Close() on an empty *LeasedServices = %v, want nil", err)
	}
}
