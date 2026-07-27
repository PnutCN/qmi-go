package netcfg

import "fmt"

// ifnamsizMax is IFNAMSIZ (16) minus the trailing NUL — the longest legal
// Linux netdev name.
const ifnamsizMax = 15

// masterSuffix is deliberately 2 bytes: it is applied to names that may
// already be at or near ifnamsizMax (USB topology paths like "wwp0s20u1i4"
// are 11 bytes today, but longer hub chains are possible), and unlike the
// data/IMS names it has no downstream consumer that needs it to be
// memorable — only qmi/add_mux, qmi/del_mux, qmi/raw_ip under it.
const masterSuffix = "_q"

// imsSuffix names the VoLTE IMS PDN's netdev relative to the device's
// original (default-data) name, so an operator or log reader can tell at a
// glance which mux belongs to which device without cross-referencing mux
// IDs.
const imsSuffix = "_ims"

// DeriveMuxNames returns the name the physical master interface must move to
// (masterName) once QMAP muxing starts, and confirms the default-data mux
// keeps the original name (dataName == original). The default data
// connection's netdev identity — and everything downstream of it (routes,
// DNS, UCI, proxy bindings) — is unchanged across the native/muxed
// transition; only the physical master (used solely for the
// add_mux/del_mux/raw_ip sysfs triad) moves to a new name.
func DeriveMuxNames(original string) (masterName, dataName string, err error) {
	masterName = original + masterSuffix
	if len(masterName) > ifnamsizMax {
		return "", "", fmt.Errorf("netcfg: 网卡名 %q 加上后缀 %q 超过 IFNAMSIZ 限制(%d 字节)", original, masterSuffix, ifnamsizMax)
	}
	return masterName, original, nil
}

// IMSInterfaceName returns the VoLTE IMS PDN's netdev name for a device
// whose default-data name is original. Callers that must not silently
// truncate should use ValidatedIMSInterfaceName instead.
func IMSInterfaceName(original string) string {
	return original + imsSuffix
}

// ValidatedIMSInterfaceName is IMSInterfaceName but rejects names that would
// exceed IFNAMSIZ instead of letting the kernel truncate them (a silent
// truncation could collide with another device's interface name).
func ValidatedIMSInterfaceName(original string) (string, error) {
	name := IMSInterfaceName(original)
	if len(name) > ifnamsizMax {
		return "", fmt.Errorf("netcfg: 网卡名 %q 加上后缀 %q 超过 IFNAMSIZ 限制(%d 字节)", original, imsSuffix, ifnamsizMax)
	}
	return name, nil
}
