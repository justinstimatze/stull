package compile

import (
	"fmt"

	"github.com/justinstimatze/stull/spec"
)

// MergeHooks merges machine m's hook entries (each invoking binary) into an
// existing settings.json object, idempotently and without clobbering anything
// else in the file. For each trigger the machine uses it appends this machine's
// entry to whatever is already registered for that trigger, and it is a no-op
// for a (trigger, command) pair that is already present — so re-running install
// never double-registers. existing may be nil (treated as empty); every key of
// existing other than the merged trigger lists is preserved verbatim. It returns
// the merged object, the count of trigger-entries newly added (0 == already
// fully installed), and any validation error from the machine.
//
// This is the pure core behind `stull install`: file IO and backups live in the
// CLI, the merge logic — the part that could silently corrupt a user's settings
// — is here, where it is unit-tested directly.
func MergeHooks(existing map[string]any, m spec.Machine, binary string) (map[string]any, int, error) {
	frag, err := SettingsFragment(m, binary)
	if err != nil {
		return nil, 0, err
	}

	out := map[string]any{}
	for k, v := range existing {
		out[k] = v
	}
	merged := map[string]any{}
	if hooks, ok := out["hooks"].(map[string]any); ok {
		for k, v := range hooks {
			merged[k] = v
		}
	}

	command := fmt.Sprintf("%s run --machine %s", binary, m.Name)
	added := 0
	for trig, entriesAny := range frag["hooks"].(map[string]any) {
		newEntries, _ := entriesAny.([]any)
		cur, _ := merged[trig].([]any)
		if hookCommandPresent(cur, command) {
			merged[trig] = cur // already installed for this trigger: idempotent
			continue
		}
		merged[trig] = append(cur, newEntries...)
		added++
	}
	out["hooks"] = merged
	return out, added, nil
}

// hookCommandPresent reports whether any entry in a trigger's list already
// registers command — the idempotency check. It tolerates whatever arbitrary
// shapes a user's hand-edited settings.json may hold (anything not matching the
// expected nesting is simply skipped, never panicked on).
func hookCommandPresent(entries []any, command string) bool {
	for _, e := range entries {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		hs, ok := em["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range hs {
			if hm, ok := h.(map[string]any); ok && hm["command"] == command {
				return true
			}
		}
	}
	return false
}
