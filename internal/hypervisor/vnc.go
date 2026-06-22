package hypervisor

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"
)

// shiftKeysym is the X11 keysym for the left Shift modifier.
const shiftKeysym = 0xffe1

// keyToKeysym maps a *base* (unshifted) QEMU sendkey token name (as produced by
// internal/deploy/bootcmd.go KeysForText) to its X11 keysym. For printable
// ASCII the keysym equals the Unicode/ASCII code point.
//
// Shifted characters are NOT stored as composed keysyms here. A "shift-" token
// is typed by holding a real Shift key around the base key (see resolveKey /
// sendVNCKeys): VMware Workstation's VNC server does not synthesize Shift from
// a composed keysym (e.g. sending 'A'/0x41 alone yields 'a'), so the shift
// modifier must be pressed explicitly.
var keyToKeysym = map[string]uint32{
	// letters a–z
	"a": 'a', "b": 'b', "c": 'c', "d": 'd', "e": 'e',
	"f": 'f', "g": 'g', "h": 'h', "i": 'i', "j": 'j',
	"k": 'k', "l": 'l', "m": 'm', "n": 'n', "o": 'o',
	"p": 'p', "q": 'q', "r": 'r', "s": 's', "t": 't',
	"u": 'u', "v": 'v', "w": 'w', "x": 'x', "y": 'y',
	"z": 'z',

	// digits 0–9
	"0": '0', "1": '1', "2": '2', "3": '3', "4": '4',
	"5": '5', "6": '6', "7": '7', "8": '8', "9": '9',

	// control
	"ret": 0xff0d,
	"spc": 0x20,

	// punctuation (base, unshifted). Shifted symbols are produced by holding
	// Shift over these same base keys: ';'→':', '-'→'_', '='→'+', '/'→'?',
	// '`'→'~', '\''→'"', and the shifted digits ('7'→'&', etc.).
	"slash":        '/',
	"dot":          '.',
	"comma":        ',',
	"semicolon":    ';',
	"equal":        '=',
	"minus":        '-',
	"apostrophe":   '\'',
	"grave_accent": '`',
}

// resolveKey maps a QEMU sendkey token to its base keysym and whether Shift
// must be held while typing it. A "shift-" prefix means "hold Shift and press
// the base key" (the base is the remainder after the prefix). Unknown tokens
// return ok=false.
func resolveKey(token string) (keysym uint32, shift bool, ok bool) {
	base := token
	if strings.HasPrefix(token, "shift-") {
		shift = true
		base = strings.TrimPrefix(token, "shift-")
	}
	keysym, ok = keyToKeysym[base]
	return keysym, shift, ok
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
	// For "shift-" tokens we press a real Shift key around the base key so the
	// VMware VNC server emits the shifted character (it does not synthesize
	// Shift from a composed keysym). Small intra-key delays mirror the timing
	// that was validated against VMware Workstation's GRUB console.
	for _, key := range keys {
		keysym, shift, ok := resolveKey(key)
		if !ok {
			continue // unknown key — skip silently
		}
		if shift {
			if err := writeKeyEvent(conn, shiftKeysym, 1); err != nil {
				return fmt.Errorf("vnc: send Shift down for %q: %w", key, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err := writeKeyEvent(conn, keysym, 1); err != nil {
			return fmt.Errorf("vnc: send KeyEvent down for %q: %w", key, err)
		}
		time.Sleep(20 * time.Millisecond)
		if err := writeKeyEvent(conn, keysym, 0); err != nil {
			return fmt.Errorf("vnc: send KeyEvent up for %q: %w", key, err)
		}
		if shift {
			time.Sleep(20 * time.Millisecond)
			if err := writeKeyEvent(conn, shiftKeysym, 0); err != nil {
				return fmt.Errorf("vnc: send Shift up for %q: %w", key, err)
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

// writeKeyEvent sends a single RFB KeyEvent:
// type(u8=4) down-flag(u8) padding(u16=0) keysym(u32).
func writeKeyEvent(conn net.Conn, keysym uint32, down uint8) error {
	msg := make([]byte, 8)
	msg[0] = 4    // KeyEvent message type
	msg[1] = down // down flag (msg[2], msg[3] = 0 padding)
	binary.BigEndian.PutUint32(msg[4:], keysym)
	_, err := conn.Write(msg)
	return err
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
