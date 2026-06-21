package hypervisor

import "testing"

// TestQemuKeyToKeysymMappings verifies that qemuKeyToKeysym contains correct
// mappings for a representative subset of keys used by the boot-command
// orchestrator.
func TestQemuKeyToKeysymMappings(t *testing.T) {
	cases := []struct {
		key    string
		keysym uint32
	}{
		{"c", 'c'},
		{"shift-semicolon", ':'},
		{"slash", '/'},
		{"ret", 0xff0d},
		{"spc", 0x20},
		{"shift-a", 'A'},
		{"shift-z", 'Z'},
		{"dot", '.'},
		{"minus", '-'},
		{"shift-minus", '_'},
		{"equal", '='},
		{"shift-equal", '+'},
		{"shift-slash", '?'},
		{"shift-7", '&'},
		{"shift-5", '%'},
		{"shift-3", '#'},
		{"shift-9", '('},
		{"shift-0", ')'},
		{"shift-grave_accent", '~'},
		{"apostrophe", '\''},
		{"shift-apostrophe", '"'},
		{"0", '0'},
		{"9", '9'},
	}

	for _, tc := range cases {
		got, ok := qemuKeyToKeysym[tc.key]
		if !ok {
			t.Errorf("qemuKeyToKeysym[%q]: missing entry", tc.key)
			continue
		}
		if got != tc.keysym {
			t.Errorf("qemuKeyToKeysym[%q] = 0x%04x, want 0x%04x", tc.key, got, tc.keysym)
		}
	}
}
