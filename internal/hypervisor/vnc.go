package hypervisor

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// qemuKeyToKeysym maps QEMU sendkey token names (as produced by
// internal/deploy/bootcmd.go KeysForText) to X11 keysyms for RFB KeyEvent
// messages. For printable ASCII the keysym equals the Unicode/ASCII code point.
// Shifted tokens map directly to the target character's keysym (e.g.
// "shift-a" → 'A' = 0x41) — the VNC backend does NOT emulate a separate Shift
// key press; the keysym already encodes the shift.
var qemuKeyToKeysym = map[string]uint32{
	// letters a–z
	"a": 'a', "b": 'b', "c": 'c', "d": 'd', "e": 'e',
	"f": 'f', "g": 'g', "h": 'h', "i": 'i', "j": 'j',
	"k": 'k', "l": 'l', "m": 'm', "n": 'n', "o": 'o',
	"p": 'p', "q": 'q', "r": 'r', "s": 's', "t": 't',
	"u": 'u', "v": 'v', "w": 'w', "x": 'x', "y": 'y',
	"z": 'z',

	// shifted letters (A–Z keysym = ASCII uppercase)
	"shift-a": 'A', "shift-b": 'B', "shift-c": 'C', "shift-d": 'D', "shift-e": 'E',
	"shift-f": 'F', "shift-g": 'G', "shift-h": 'H', "shift-i": 'I', "shift-j": 'J',
	"shift-k": 'K', "shift-l": 'L', "shift-m": 'M', "shift-n": 'N', "shift-o": 'O',
	"shift-p": 'P', "shift-q": 'Q', "shift-r": 'R', "shift-s": 'S', "shift-t": 'T',
	"shift-u": 'U', "shift-v": 'V', "shift-w": 'W', "shift-x": 'X', "shift-y": 'Y',
	"shift-z": 'Z',

	// digits 0–9
	"0": '0', "1": '1', "2": '2', "3": '3', "4": '4',
	"5": '5', "6": '6', "7": '7', "8": '8', "9": '9',

	// control
	"ret": 0xff0d,
	"spc": 0x20,

	// unshifted punctuation
	"slash":      '/',
	"dot":        '.',
	"comma":      ',',
	"semicolon":  ';',
	"equal":      '=',
	"minus":      '-',
	"apostrophe": '\'',

	// shifted punctuation — map to the resulting character's keysym directly
	"shift-semicolon":    ':',
	"shift-minus":        '_',
	"shift-equal":        '+',
	"shift-slash":        '?',
	"shift-7":            '&',
	"shift-5":            '%',
	"shift-3":            '#',
	"shift-9":            '(',
	"shift-0":            ')',
	"shift-grave_accent": '~',
	"shift-apostrophe":   '"',
}

// sendVNCKeys connects to a VNC server at addr (host:port), performs the
// minimal RFB 3.8 handshake (None security), then injects one down+up
// KeyEvent per entry in keys. Unknown key names are silently skipped.
//
// The function does NOT keep the connection open between calls; each call
// opens and closes its own TCP connection. This is intentional: the caller
// (SendKeys on Workstation) is invoked infrequently and the simplicity is
// worth the overhead.
func sendVNCKeys(ctx context.Context, addr string, keys []string) error {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("vnc: dial %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	// Set a generous deadline; key injection is interactive so we don't want
	// a partial send hanging the orchestrator forever.
	if err := conn.SetDeadline(time.Now().Add(60 * time.Second)); err != nil {
		return fmt.Errorf("vnc: set deadline: %w", err)
	}

	// ---- RFB handshake ----

	// 1. Server sends 12-byte protocol version: "RFB 003.00x\n"
	serverVer := make([]byte, 12)
	if _, err := readFull(conn, serverVer); err != nil {
		return fmt.Errorf("vnc: read server version: %w", err)
	}

	// 2. Client responds with RFB 3.8.
	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		return fmt.Errorf("vnc: write client version: %w", err)
	}

	// 3. Security: read number of supported types (1 byte) then the types.
	nSecBuf := make([]byte, 1)
	if _, err := readFull(conn, nSecBuf); err != nil {
		return fmt.Errorf("vnc: read security count: %w", err)
	}
	nSec := int(nSecBuf[0])
	secTypes := make([]byte, nSec)
	if nSec > 0 {
		if _, err := readFull(conn, secTypes); err != nil {
			return fmt.Errorf("vnc: read security types: %w", err)
		}
	}
	// Require None (type 1).
	hasNone := false
	for _, t := range secTypes {
		if t == 1 {
			hasNone = true
			break
		}
	}
	if !hasNone {
		return fmt.Errorf("vnc: server does not offer None (type 1) security")
	}
	if _, err := conn.Write([]byte{1}); err != nil {
		return fmt.Errorf("vnc: write security type: %w", err)
	}

	// 4. SecurityResult: 4 bytes, big-endian; 0 = OK.
	var secResult uint32
	if err := binary.Read(conn, binary.BigEndian, &secResult); err != nil {
		return fmt.Errorf("vnc: read SecurityResult: %w", err)
	}
	if secResult != 0 {
		return fmt.Errorf("vnc: SecurityResult = %d (non-zero)", secResult)
	}

	// 5. ClientInit: 1 byte, shared-flag = 1 (allow other viewers).
	if _, err := conn.Write([]byte{1}); err != nil {
		return fmt.Errorf("vnc: write ClientInit: %w", err)
	}

	// 6. ServerInit: width(u16) + height(u16) + 16-byte pixel-format +
	//    name-length(u32) + name bytes. Discard all of it.
	siFixed := make([]byte, 2+2+16) // width, height, pixel-format
	if _, err := readFull(conn, siFixed); err != nil {
		return fmt.Errorf("vnc: read ServerInit fixed: %w", err)
	}
	var nameLen uint32
	if err := binary.Read(conn, binary.BigEndian, &nameLen); err != nil {
		return fmt.Errorf("vnc: read ServerInit name-length: %w", err)
	}
	if nameLen > 0 {
		nameBuf := make([]byte, nameLen)
		if _, err := readFull(conn, nameBuf); err != nil {
			return fmt.Errorf("vnc: read ServerInit name: %w", err)
		}
	}

	// ---- KeyEvent injection ----
	// RFB KeyEvent: type(u8=4) down-flag(u8) padding(u16=0) keysym(u32)
	for _, key := range keys {
		keysym, ok := qemuKeyToKeysym[key]
		if !ok {
			continue // unknown key — skip silently
		}
		for _, down := range []uint8{1, 0} {
			msg := make([]byte, 8)
			msg[0] = 4    // KeyEvent message type
			msg[1] = down // down flag
			// msg[2], msg[3] = 0 (padding)
			binary.BigEndian.PutUint32(msg[4:], keysym)
			if _, err := conn.Write(msg); err != nil {
				return fmt.Errorf("vnc: send KeyEvent for %q: %w", key, err)
			}
		}
		// Brief inter-key delay so the guest firmware processes each event.
		select {
		case <-ctx.Done():
			return fmt.Errorf("vnc: context cancelled: %w", ctx.Err())
		case <-time.After(40 * time.Millisecond):
		}
	}

	return nil
}

// readFull reads exactly len(buf) bytes, blocking until all are received or an
// error occurs. This wraps net.Conn reads which may return short reads.
func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
