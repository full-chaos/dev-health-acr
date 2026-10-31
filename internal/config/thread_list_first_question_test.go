package config

import "testing"

// CHAOS-12235: the thread list's first_question switch is off unless a
// deployment turns it on, a value that is not a boolean is refused, and the
// startup attributes report it.
func TestTheThreadListFirstQuestionSwitchDefaultsOff(t *testing.T) {
	if ThreadListFirstQuestionEnabledEnvVar != "ACR_THREAD_LIST_FIRST_QUESTION_ENABLED" {
		t.Fatalf("env var %q, want ACR_THREAD_LIST_FIRST_QUESTION_ENABLED", ThreadListFirstQuestionEnabledEnvVar)
	}
	for _, tc := range []struct {
		value   string
		set     bool
		want    bool
		wantErr bool
	}{
		{set: false, want: false},
		{value: "true", set: true, want: true},
		{value: "false", set: true, want: false},
		{value: "sometimes", set: true, wantErr: true},
	} {
		lookup := func(key string) (string, bool) {
			if key == "ACR_THREAD_LIST_FIRST_QUESTION_ENABLED" && tc.set {
				return tc.value, true
			}
			return "", false
		}
		var cfg Config
		err := loadHostedRuntimeValues(lookup, &cfg, false, false, false)
		if tc.wantErr {
			if err == nil {
				t.Errorf("value %q: no error", tc.value)
			}
			continue
		}
		if err != nil {
			t.Fatalf("value %q: %v", tc.value, err)
		}
		if cfg.ThreadListFirstQuestionEnabled != tc.want {
			t.Errorf("value %q (set=%v): enabled = %v, want %v", tc.value, tc.set, cfg.ThreadListFirstQuestionEnabled, tc.want)
		}
	}
	for _, enabled := range []bool{true, false} {
		attrs := Config{ThreadListFirstQuestionEnabled: enabled}.SafeAttributes()
		found := false
		for i := 0; i+1 < len(attrs); i += 2 {
			if attrs[i] == "thread_list_first_question_enabled" && attrs[i+1] == enabled {
				found = true
			}
		}
		if !found {
			t.Fatalf("the startup attributes do not report the switch as %v", enabled)
		}
	}
}
