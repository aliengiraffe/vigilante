package backend

import (
	"strings"

	"github.com/nicobistolfi/vigilante/internal/state"
)

// SelectNextWorkItem returns the first eligible work item for dispatch, or nil.
func SelectNextWorkItem(items []WorkItem, sessions []state.Session, target state.WatchTarget) *WorkItem {
	selected := SelectWorkItems(items, sessions, target, 1)
	if len(selected) == 0 {
		return nil
	}
	return &selected[0]
}

// SelectWorkItems filters work items that are eligible for dispatch according
// to target labels, session overlap, and the requested limit.
func SelectWorkItems(items []WorkItem, sessions []state.Session, target state.WatchTarget, limit int) []WorkItem {
	if limit <= 0 {
		return nil
	}

	active := map[int]bool{}
	for _, session := range sessions {
		if session.Repo == target.Repo && sessionPreventsRedispatch(session) {
			active[session.IssueNumber] = true
		}
	}

	selected := make([]WorkItem, 0, limit)
	for i := range items {
		if len(selected) >= limit {
			break
		}
		if active[items[i].Number] {
			continue
		}
		if !matchesLabelAllowlist(items[i], target.Labels) {
			continue
		}
		selected = append(selected, items[i])
		active[items[i].Number] = true
	}
	return selected
}

// ActiveSessionCount returns the number of sessions for the given target
// that consume dispatch capacity (running or resuming).
func ActiveSessionCount(sessions []state.Session, target state.WatchTarget) int {
	count := 0
	for _, session := range sessions {
		if session.Repo == target.Repo && sessionConsumesDispatchCapacity(session) {
			count++
		}
	}
	return count
}

// HasLabel reports whether the label list contains any of the wanted labels.
func HasLabel(labels []string, wanted ...string) bool {
	for _, label := range labels {
		for _, candidate := range wanted {
			if label == candidate {
				return true
			}
		}
	}
	return false
}

// PlanLabelSync calculates which labels to add and remove to reach the
// desired state from current, considering only labels in the managed set.
func PlanLabelSync(current []string, desired []string, managed []string) (toAdd []string, toRemove []string) {
	managedSet := make(map[string]struct{}, len(managed))
	for _, label := range managed {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		managedSet[label] = struct{}{}
	}

	currentSet := make(map[string]struct{}, len(current))
	for _, label := range current {
		name := strings.TrimSpace(label)
		if name == "" {
			continue
		}
		currentSet[name] = struct{}{}
	}

	desiredSet := make(map[string]struct{}, len(desired))
	for _, label := range desired {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		desiredSet[label] = struct{}{}
	}

	toAdd = make([]string, 0, len(desiredSet))
	for label := range desiredSet {
		if _, ok := currentSet[label]; ok {
			continue
		}
		toAdd = append(toAdd, label)
	}

	toRemove = make([]string, 0, len(managedSet))
	for label := range managedSet {
		if _, ok := currentSet[label]; !ok {
			continue
		}
		if _, ok := desiredSet[label]; ok {
			continue
		}
		toRemove = append(toRemove, label)
	}

	return toAdd, toRemove
}

func sessionPreventsRedispatch(session state.Session) bool {
	if session.StaleAutoRestartStoppedAt != "" {
		return true
	}
	if sessionConsumesDispatchCapacity(session) || session.Status == state.SessionStatusBlocked {
		return true
	}
	if session.Status != state.SessionStatusSuccess {
		return false
	}
	if session.CleanupCompletedAt != "" || session.MonitoringStoppedAt != "" {
		return false
	}
	return true
}

func sessionConsumesDispatchCapacity(session state.Session) bool {
	return session.Status == state.SessionStatusRunning || session.Status == state.SessionStatusResuming
}

func matchesLabelAllowlist(item WorkItem, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}

	for _, configured := range allowlist {
		for _, label := range item.Labels {
			if label == configured {
				return true
			}
		}
	}
	return false
}
