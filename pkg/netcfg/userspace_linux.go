//go:build linux
// +build linux

package netcfg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ipv6InterfaceSysctlRoot = "/proc/sys/net/ipv6/conf"

// PrepareUserspaceOnly keeps the kernel from claiming an interface whose IP
// layer is owned by a userspace netstack.
func PrepareUserspaceOnly(ifname string) error {
	ifname = strings.TrimSpace(ifname)
	if ifname == "" || filepath.Base(ifname) != ifname {
		return fmt.Errorf("netcfg: invalid interface name %q", ifname)
	}
	if err := disableIPv6AutoconfigurationAt(ipv6InterfaceSysctlRoot, ifname); err != nil {
		return err
	}

	configurator := &LinuxConfigurator{}
	return errors.Join(
		configurator.FlushRoutes(ifname),
		configurator.FlushAddresses(ifname),
	)
}

func disableIPv6AutoconfigurationAt(root, ifname string) error {
	var result error
	for _, setting := range []string{"accept_ra", "accept_ra_defrtr", "autoconf"} {
		path := filepath.Join(root, ifname, setting)
		if err := os.WriteFile(path, []byte("0\n"), 0); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			result = errors.Join(result, fmt.Errorf("netcfg: disable IPv6 %s on %s: %w", setting, ifname, err))
		}
	}
	return result
}
