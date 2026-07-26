// Command ims-probe validates that a modem without native VoLTE can still
// establish a dedicated IMS APN PDN over a QMAP mux, and reports whether the
// network delivers P-CSCF addresses and the IMCN flag.
//
// This is a throwaway hardware probe, not a production tool: it exists only
// to answer the go/no-go question for assumptions A1 (an IMS-APN PDN can
// coexist with the default data PDN over a QMAP mux) and A2 (the network
// will hand out an ims APN PDN to a modem that does not advertise native
// VoLTE support). See docs/verification/2026-07-26-ims-pdn-probe.md for the
// recorded result of running this against real hardware.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/iniwex5/qmi-go/pkg/netcfg"
	"github.com/iniwex5/qmi-go/pkg/qmi"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run does all the work. Splitting main() into run() error lets every
// resource acquired along the way (QMI client, WDA/WDS clients, the QMAP
// mux, the IMS PDN handle) be released via defer on EVERY exit path,
// including failures: a bare log.Fatalf calls os.Exit and skips already
// registered defers, which would leak the QMAP mux (and possibly leave the
// IMS PDN up) on exactly the failure paths this probe cares most about.
func run() error {
	devicePath := flag.String("device", "/dev/cdc-wdm0", "QMI control device")
	iface := flag.String("iface", "wwan0", "master WWAN interface")
	apn := flag.String("apn", "ims", "APN to activate")
	muxID := flag.Uint("mux", 1, "QMAP mux ID for the IMS PDN")
	ipFamily := flag.Uint("ip-family", 4, "4 or 6")
	hold := flag.Duration("hold", 30*time.Second, "how long to hold the PDN up")
	epIface := flag.Uint("ep-iface", 4, "WDA endpoint interface number used as MuxBinding.EpIfID when "+
		"binding the WDS client to the QMAP mux. The WDA Get Data Format response on this modem only "+
		"exposes EndpointType/EndpointID, not a confirmed interface-number field, so this value cannot "+
		"be discovered automatically -- it must be confirmed on real hardware and this flag adjusted if "+
		"BindMuxDataPort fails. 4 is the common USB interface number for Quectel QMI endpoints (EC20/EC25).")
	flag.Parse()

	client, err := qmi.NewClientWithOptions(context.Background(), *devicePath, qmi.ClientOptions{})
	if err != nil {
		return fmt.Errorf("open QMI client: %w", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	wda, err := qmi.NewWDAService(client)
	if err != nil {
		return fmt.Errorf("WDA service: %w", err)
	}
	defer wda.Close()

	details, err := wda.GetDataFormatDetails(ctx)
	if err != nil {
		return fmt.Errorf("get data format details: %w", err)
	}
	// NOTE: details.EndpointID is printed for reference only. It is NOT
	// assumed to be the USB interface number BindMuxDataPort needs -- that
	// mapping is unconfirmed on this modem/codebase, which is why -ep-iface
	// exists below instead of silently using EndpointID.
	fmt.Printf("endpoint type=%d endpoint id=%d link-protocol=%d\n",
		details.EndpointType, details.EndpointID, details.LinkProtocol)
	fmt.Printf("using ep-iface=%d for MuxBinding.EpIfID (override with -ep-iface if the bind fails on this modem)\n",
		*epIface)

	muxIface, err := netcfg.AddQMAPMux(*iface, uint8(*muxID))
	if err != nil {
		return fmt.Errorf("add QMAP mux (A1 FAILED): %w", err)
	}
	fmt.Printf("mux interface: %s\n", muxIface)
	defer func() {
		if err := netcfg.DelQMAPMux(*iface, uint8(*muxID)); err != nil {
			log.Printf("WARNING: leaked mux %d: %v", *muxID, err)
		}
	}()

	wds, err := qmi.NewWDSService(client)
	if err != nil {
		return fmt.Errorf("WDS service: %w", err)
	}
	defer wds.Close()

	binding := qmi.MuxBinding{
		EpType:     details.EndpointType,
		EpIfID:     uint32(*epIface),
		MuxID:      uint8(*muxID),
		ClientType: 0x01, // QMI_WDS_CLIENT_TYPE_TETHERED
	}
	if err := wds.BindMuxDataPort(ctx, binding); err != nil {
		return fmt.Errorf("bind mux data port (A1 FAILED): %w", err)
	}
	fmt.Println("bound WDS client to mux")

	family := uint8(0x04)
	if *ipFamily == 6 {
		family = 0x06
	}

	handle, err := wds.StartNetworkInterface(ctx, *apn, "", "", 0, family)
	if err != nil {
		return fmt.Errorf("start network on APN %q (A2 FAILED): %w", *apn, err)
	}
	fmt.Printf("A2 PASSED: IMS PDN up, handle=%d\n", handle)
	defer func() {
		if err := wds.StopNetworkInterface(ctx, handle); err != nil {
			log.Printf("WARNING: stop network: %v", err)
		}
	}()

	settings, err := wds.GetRuntimeSettings(ctx, family)
	if err != nil {
		return fmt.Errorf("get runtime settings: %w", err)
	}

	fmt.Printf("IPv4=%v IPv6=%v MTU=%d\n", settings.IPv4Address, settings.IPv6Address, settings.MTU)
	fmt.Printf("IMCN flag: %v\n", settings.IMCN)
	fmt.Printf("P-CSCF via PCO: %v\n", settings.PCSCFUsingPCO)
	fmt.Printf("P-CSCF addresses: %v\n", settings.PCSCFv4)
	fmt.Printf("P-CSCF domains: %v\n", settings.PCSCFDomains)

	switch {
	case len(settings.PCSCFv4) > 0:
		fmt.Println("=> P-CSCF tier 1 (PCO) available")
	case len(settings.PCSCFDomains) > 0:
		fmt.Println("=> P-CSCF tier 2 (FQDN) available, resolution required")
	default:
		fmt.Println("=> no P-CSCF from the network; tier 3 (carrier preset) required")
	}

	fmt.Printf("holding PDN for %s — verify the default data PDN is still up\n", *hold)
	time.Sleep(*hold)
	return nil
}
