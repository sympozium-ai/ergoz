// Package gpumetrics decodes the amdgpu `gpu_metrics` binary sysfs blob.
//
// The blob is the kernel's SMU metrics table (drivers/gpu/drm/amd/include/
// kgd_pp_interface.h), exposed world-readable at
// <hwmon device>/gpu_metrics. Layout follows natural C alignment; the v3.0
// struct (Strix-class APUs) is 264 bytes including trailing padding —
// verified against real hardware where structure_size reads 264 and
// average_socket_power agrees with hwmon power1_input within 0.2 W.
//
// Only fields that survived empirical validation are exported as trusted;
// fields observed to be unreliable on real ASICs (sentinel 0xFFFF limits,
// average_sys_power below socket power, average_all_core_power disagreeing
// with the per-core sum ~2x) are either dropped or recomputed. Do not add a
// field to the trusted set without hardware evidence.
package gpumetrics

import (
	"encoding/binary"
	"fmt"
)

// sentinel16 marks an unpopulated uint16 field in SMU tables.
const sentinel16 = 0xFFFF

// header offsets (metrics_table_header)
const (
	offStructureSize   = 0 // u16
	offFormatRevision  = 2 // u8
	offContentRevision = 3 // u8
)

// gpu_metrics_v3_0 field offsets, natural C alignment (verified: total 264).
const (
	v3Size = 264

	offV3SocketPower  = 112 // u32, mW — PPT/STAPM (APU+dGPU)
	offV3IPUPower     = 116 // u16, mW — NPU
	offV3APUPower     = 120 // u32, mW (2 bytes padding after ipu_power)
	offV3GfxPower     = 124 // u32, mW — GPU-only
	offV3DGPUPower    = 128 // u32, mW
	offV3AllCorePower = 132 // u32, mW — observed unreliable; recomputed
	offV3CorePower    = 136 // u16[16], mW per core
	offV3GfxActivity  = 42  // u16, %
)

// Metrics is the validated, unit-converted subset of gpu_metrics v3.0.
// All powers are in watts. A nil pointer means the field was absent or
// failed validation on this ASIC.
type Metrics struct {
	FormatRevision  uint8
	ContentRevision uint8

	// SocketPowerW is PPT/STAPM socket power (CPU+GPU+NPU on APUs).
	SocketPowerW *float64
	// GfxPowerW is GPU-only power, isolated from the socket total.
	GfxPowerW *float64
	// NPUPowerW is IPU/NPU power. Zero is a valid reading at NPU idle.
	NPUPowerW *float64
	// CoreSumPowerW is the sum of per-core CPU power. Preferred over the
	// blob's average_all_core_power, which disagrees with this sum on
	// real hardware.
	CoreSumPowerW *float64
	// GfxActivityPct is GFX busy percent [0,100].
	GfxActivityPct *float64
}

// Supported reports whether the blob's header revision is one this package
// can decode. Callers should fall back to hwmon-only sampling when false.
func Supported(blob []byte) bool {
	if len(blob) < 4 {
		return false
	}
	return blob[offFormatRevision] == 3
}

// Decode parses a gpu_metrics blob. Unknown revisions and truncated blobs
// return an error; the caller must treat that as "no decomposition
// available", never as zero power.
func Decode(blob []byte) (*Metrics, error) {
	if len(blob) < 4 {
		return nil, fmt.Errorf("gpu_metrics blob too short: %d bytes", len(blob))
	}
	size := binary.LittleEndian.Uint16(blob[offStructureSize:])
	format := blob[offFormatRevision]
	content := blob[offContentRevision]

	if format != 3 {
		return nil, fmt.Errorf("unsupported gpu_metrics format revision %d.%d", format, content)
	}
	if int(size) > len(blob) || size < v3Size {
		return nil, fmt.Errorf("gpu_metrics v3 structure_size %d, want >= %d (got %d bytes)", size, v3Size, len(blob))
	}

	m := &Metrics{FormatRevision: format, ContentRevision: content}

	if w, ok := u32MilliToWatts(blob, offV3SocketPower); ok {
		m.SocketPowerW = &w
	}
	if w, ok := u32MilliToWatts(blob, offV3GfxPower); ok {
		m.GfxPowerW = &w
	}
	if w, ok := u16MilliToWatts(blob, offV3IPUPower); ok {
		m.NPUPowerW = &w
	}

	var coreSum float64
	coreValid := false
	for i := 0; i < 16; i++ {
		v := binary.LittleEndian.Uint16(blob[offV3CorePower+2*i:])
		if v == sentinel16 {
			continue
		}
		coreSum += float64(v) / 1000.0
		coreValid = true
	}
	if coreValid {
		m.CoreSumPowerW = &coreSum
	}

	if a := binary.LittleEndian.Uint16(blob[offV3GfxActivity:]); a != sentinel16 && a <= 100 {
		pct := float64(a)
		m.GfxActivityPct = &pct
	}

	// Cross-field sanity: gfx power can never exceed socket power on an
	// APU. If it does, the decomposition is untrustworthy on this ASIC —
	// drop it rather than export a wrong number.
	if m.SocketPowerW != nil && m.GfxPowerW != nil && *m.GfxPowerW > *m.SocketPowerW {
		m.GfxPowerW = nil
	}

	return m, nil
}

func u32MilliToWatts(blob []byte, off int) (float64, bool) {
	v := binary.LittleEndian.Uint32(blob[off:])
	if v == 0xFFFFFFFF {
		return 0, false
	}
	return float64(v) / 1000.0, true
}

func u16MilliToWatts(blob []byte, off int) (float64, bool) {
	v := binary.LittleEndian.Uint16(blob[off:])
	if v == sentinel16 {
		return 0, false
	}
	return float64(v) / 1000.0, true
}
