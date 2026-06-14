package hypervisor

import (
	"strings"
	"testing"
)

// TestSendKeysChunkFitsCmdLimit guards maxScancodesPerCall: one full chunk,
// rendered as a PowerShell byte-array literal inside the SendKeys script and
// then encoded the way the winrm library ships it (powershell -EncodedCommand
// <base64(UTF-16LE)>, wrapped in cmd.exe), must stay under cmd.exe's ~8 KB
// command-line limit — otherwise SendKeys fails with "The command line is too
// long."
func TestSendKeysChunkFitsCmdLimit(t *testing.T) {
	const cmdLimit = 8192

	// Largest possible array literal: every byte renders as "0xXX" (4 chars)
	// plus a comma separator.
	var sb strings.Builder
	sb.WriteString("[byte[]]@(")
	for i := 0; i < maxScancodesPerCall; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("0xAA")
	}
	sb.WriteByte(')')

	// Full script ≈ fixed boilerplate (~480 chars) + the array literal.
	scriptLen := 480 + sb.Len()
	// winrm encodes the script UTF-16LE (×2) then base64 (4 chars per 3 bytes).
	encoded := ((scriptLen*2 + 2) / 3) * 4
	if encoded >= cmdLimit {
		t.Fatalf("encoded SendKeys chunk command line = %d bytes, must stay under %d; lower maxScancodesPerCall (=%d)",
			encoded, cmdLimit, maxScancodesPerCall)
	}
}

// TestBuildScancodesLongLineNeedsChunking is a regression guard for the
// "command line too long" fix: a realistic GRUB line produces far more scancode
// bytes than one chunk, so SendKeys must actually split it.
func TestBuildScancodesLongLineNeedsChunking(t *testing.T) {
	keys := make([]string, 0, 300)
	for i := 0; i < 300; i++ {
		keys = append(keys, "a") // 'a' → 2 bytes (make+break) each
	}
	if got := len(buildScancodes(keys)); got <= maxScancodesPerCall {
		t.Fatalf("expected > %d scancode bytes for a long line, got %d", maxScancodesPerCall, got)
	}
}
