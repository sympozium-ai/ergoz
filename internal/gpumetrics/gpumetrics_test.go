package gpumetrics

import (
	"encoding/binary"
	"testing"
)

// buildV3 constructs a synthetic 264-byte v3.0 blob with known values at the
// verified offsets.
func buildV3(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 264)
	binary.LittleEndian.PutUint16(b[0:], 264) // structure_size
	b[2] = 3                                  // format_revision
	b[3] = 0                                  // content_revision

	binary.LittleEndian.PutUint32(b[112:], 140217) // socket 140.217 W
	binary.LittleEndian.PutUint16(b[116:], 0)      // NPU 0 mW (valid at idle)
	binary.LittleEndian.PutUint32(b[124:], 52322)  // gfx 52.322 W
	binary.LittleEndian.PutUint32(b[132:], 7900)   // all_core (unreliable, ignored)
	for i := 0; i < 16; i++ {
		binary.LittleEndian.PutUint16(b[136+2*i:], 1000) // 1.0 W per core (exact in float64)
	}
	binary.LittleEndian.PutUint16(b[42:], 100) // gfx activity 100%
	return b
}

func TestDecodeV3_VerifiedFields(t *testing.T) {
	m, err := Decode(buildV3(t))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if m.SocketPowerW == nil || *m.SocketPowerW != 140.217 {
		t.Fatalf("socket = %v, want 140.217", m.SocketPowerW)
	}
	if m.GfxPowerW == nil || *m.GfxPowerW != 52.322 {
		t.Fatalf("gfx = %v, want 52.322", m.GfxPowerW)
	}
	if m.NPUPowerW == nil || *m.NPUPowerW != 0 {
		t.Fatalf("npu = %v, want 0 (valid idle reading)", m.NPUPowerW)
	}
	if m.CoreSumPowerW == nil || *m.CoreSumPowerW != 16.0 {
		t.Fatalf("coreSum = %v, want 16.0", m.CoreSumPowerW)
	}
	if m.GfxActivityPct == nil || *m.GfxActivityPct != 100 {
		t.Fatalf("gfxActivity = %v, want 100", m.GfxActivityPct)
	}
}

func TestDecode_SentinelsDropped(t *testing.T) {
	b := buildV3(t)
	binary.LittleEndian.PutUint16(b[116:], 0xFFFF) // NPU sentinel
	for i := 0; i < 16; i++ {
		binary.LittleEndian.PutUint16(b[136+2*i:], 0xFFFF)
	}
	m, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if m.NPUPowerW != nil {
		t.Fatalf("npu = %v, want nil for sentinel", m.NPUPowerW)
	}
	if m.CoreSumPowerW != nil {
		t.Fatalf("coreSum = %v, want nil when all cores sentinel", m.CoreSumPowerW)
	}
}

func TestDecode_GfxAboveSocketDropped(t *testing.T) {
	b := buildV3(t)
	binary.LittleEndian.PutUint32(b[124:], 999999999) // gfx >> socket
	m, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if m.GfxPowerW != nil {
		t.Fatal("gfx power exceeding socket power must be dropped, not exported")
	}
}

func TestDecode_UnsupportedRevision(t *testing.T) {
	b := buildV3(t)
	b[2] = 2
	if _, err := Decode(b); err == nil {
		t.Fatal("expected error for format revision 2")
	}
	if Supported(b) {
		t.Fatal("Supported must be false for revision 2")
	}
}

func TestDecode_Truncated(t *testing.T) {
	if _, err := Decode(buildV3(t)[:100]); err == nil {
		t.Fatal("expected error for truncated blob")
	}
}
