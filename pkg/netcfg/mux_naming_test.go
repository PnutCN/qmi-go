package netcfg

import "testing"

func TestDeriveMuxNamesKeepsDataOnOriginalName(t *testing.T) {
	masterName, dataName, err := DeriveMuxNames("wwp0s20u1i4")
	if err != nil {
		t.Fatalf("DeriveMuxNames: %v", err)
	}
	if dataName != "wwp0s20u1i4" {
		t.Fatalf("dataName = %q, want the original name unchanged", dataName)
	}
	if masterName == "wwp0s20u1i4" {
		t.Fatal("masterName must move off the original name once muxed, or add_mux/del_mux on the data netdev would silently no-op")
	}
	if masterName != "wwp0s20u1i4_q" {
		t.Fatalf("masterName = %q, want wwp0s20u1i4_q", masterName)
	}
	if len(masterName) > 15 {
		t.Fatalf("masterName %q is %d bytes, exceeds IFNAMSIZ-1=15", masterName, len(masterName))
	}
}

func TestDeriveMuxNamesRejectsNameTooLongForMasterSuffix(t *testing.T) {
	// 14 bytes + "_q" (2) = 16, exceeds IFNAMSIZ-1=15.
	if _, _, err := DeriveMuxNames("wwp0s20u12i344"); err == nil {
		t.Fatal("expected an error for a base name too long to carry the master suffix")
	}
}

func TestIMSInterfaceNameSuffix(t *testing.T) {
	if got := IMSInterfaceName("wwp0s20u1i4"); got != "wwp0s20u1i4_ims" {
		t.Fatalf("IMSInterfaceName = %q, want wwp0s20u1i4_ims", got)
	}
}

func TestValidatedIMSInterfaceNameAcceptsNameAtTheLimit(t *testing.T) {
	// 11 bytes + "_ims" (4) = 15, exactly IFNAMSIZ-1. Verified live on this
	// machine during the architecture review for this phase.
	got, err := ValidatedIMSInterfaceName("wwp0s20u1i4")
	if err != nil {
		t.Fatalf("ValidatedIMSInterfaceName: %v", err)
	}
	if got != "wwp0s20u1i4_ims" {
		t.Fatalf("got %q, want wwp0s20u1i4_ims", got)
	}
}

func TestValidatedIMSInterfaceNameRejectsNameTooLongForIMSSuffix(t *testing.T) {
	// 12 bytes + "_ims" (4) = 16, exceeds IFNAMSIZ-1=15. Must fail loudly,
	// not silently truncate into a name that could collide with another
	// device's mux.
	if _, err := ValidatedIMSInterfaceName("wwp0s20u12i4"); err == nil {
		t.Fatal("expected an error for a base name too long to carry the _ims suffix")
	}
}
