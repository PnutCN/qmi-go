package qmi

import (
	"context"
	"fmt"
	"unicode/utf8"
)

// ReadActiveAPN 只查询当前 WDS client 所属连接，不读取可能属于上张卡的静态 profile。
// 不调用 SetIPFamilyPreference：只读认卡不能改变正在使用的连接。
func (w *WDSService) ReadActiveAPN(ctx context.Context) (string, error) {
	if w == nil || w.client == nil {
		return "", fmt.Errorf("active APN: WDS service unavailable")
	}
	resp, err := w.client.SendRequest(ctx, ServiceWDS, w.clientID, WDSGetRuntimeSettings,
		[]TLV{NewTLVUint32(0x10, RuntimeMaskAPNName)})
	if err != nil {
		return "", err
	}
	if err := resp.CheckResult(); err != nil {
		return "", fmt.Errorf("active APN query failed: %w", err)
	}
	var value []byte
	found := false
	for _, tlv := range resp.TLVs {
		if tlv.Type != 0x14 {
			continue
		}
		if found {
			return "", fmt.Errorf("active APN: duplicate name TLV")
		}
		value, found = tlv.Value, true
	}
	if !found || len(value) > 1024 || !utf8.Valid(value) {
		return "", fmt.Errorf("active APN: name absent or invalid")
	}
	for _, b := range value {
		if b < 0x20 || b == 0x7f {
			return "", fmt.Errorf("active APN: invalid name bytes")
		}
	}
	return string(value), nil
}
