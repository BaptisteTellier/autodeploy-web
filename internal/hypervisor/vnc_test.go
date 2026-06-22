package hypervisor

import "testing"

// TestResolveKey verifies that resolveKey returns the correct base keysym and
// shift flag for a representative subset of the tokens produced by the
// boot-command orchestrator. Shifted symbols resolve to their UNSHIFTED base
// key plus shift=true (the real Shift key is pressed around it), e.g.
// "shift-semicolon" → (';', shift) which types ':'.
func TestResolveKey(t *testing.T) {
	cases := []struct {
		token  string
		keysym uint32
		shift  bool
	}{
		{"c", 'c', false},
		{"shift-semicolon", ';', true}, // ':'
		{"slash", '/', false},
		{"ret", 0xff0d, false},
		{"spc", 0x20, false},
		{"shift-a", 'a', true}, // 'A'
		{"shift-z", 'z', true}, // 'Z'
		{"dot", '.', false},
		{"minus", '-', false},
		{"shift-minus", '-', true}, // '_'
		{"equal", '=', false},
		{"shift-equal", '=', true},        // '+'
		{"shift-slash", '/', true},        // '?'
		{"shift-7", '7', true},            // '&'
		{"shift-5", '5', true},            // '%'
		{"shift-3", '3', true},            // '#'
		{"shift-9", '9', true},            // '('
		{"shift-0", '0', true},            // ')'
		{"shift-grave_accent", '`', true}, // '~'
		{"apostrophe", '\'', false},
		{"shift-apostrophe", '\'', true}, // '"'
		{"0", '0', false},
		{"9", '9', false},
	}

	for _, tc := range cases {
		keysym, shift, ok := resolveKey(tc.token)
		if !ok {
			t.Errorf("resolveKey(%q): missing entry", tc.token)
			continue
		}
		if keysym != tc.keysym || shift != tc.shift {
			t.Errorf("resolveKey(%q) = (0x%04x, shift=%v), want (0x%04x, shift=%v)",
				tc.token, keysym, shift, tc.keysym, tc.shift)
		}
	}

	if _, _, ok := resolveKey("no_such_key"); ok {
		t.Errorf("resolveKey(no_such_key): expected ok=false")
	}
}
