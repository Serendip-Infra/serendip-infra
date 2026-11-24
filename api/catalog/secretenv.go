package catalog

import (
	"fmt"
	"slices"
	"strings"
)

// Spec 026: a Component's secret variables, and a Deployment's for its
// Environment, are lists in the resource document. The CLI edits them
// with flags instead of a hand-written list; an MCP tool writes the list
// in the document itself, so the flags are CLI-only.

const secretEnvCLIOnly = "an assistant writes the secretEnv list in the resource document"

var secretEnvParams = []Param{
	{Name: "secret-env", In: InEdit, Repeat: true, CLIOnly: secretEnvCLIOnly,
		Description: "Set a secret variable, NAME=SECRET_ID:KEY: the variable NAME gets key KEY of the Secret's value (repeatable; replaces NAME if it's set)"},
	{Name: "remove-secret-env", In: InEdit, Repeat: true, CLIOnly: secretEnvCLIOnly,
		Description: "Remove the secret variable NAME (repeatable)"},
}

// secretEnvPath is where each kind keeps its list, below the document's
// spec.
var secretEnvPath = map[string][]string{
	"Component":  {"secretEnv"},
	"Deployment": {"overrides", "secretEnv"},
}

// secretEnvEdit applies the secret-env flags to the list at path below
// the document's spec.
func secretEnvEdit(path []string) func(map[string]any, map[string]any) (map[string]any, error) {
	return func(doc map[string]any, args map[string]any) (map[string]any, error) {
		set, _ := args["secret-env"].([]any)
		remove, _ := args["remove-secret-env"].([]any)
		return editSecretEnv(doc, path, set, remove)
	}
}

// editSecretEnv edits doc in place. Its set and remove lists hold
// strings (Request has checked them).
func editSecretEnv(doc map[string]any, path []string, set, remove []any) (map[string]any, error) {
	// Parents are made only when something is set, and pruned again if
	// the list ends empty, so a remove never adds an empty object.
	parent := doc
	var made []func()
	for _, name := range append([]string{"spec"}, path[:len(path)-1]...) {
		next, ok := parent[name].(map[string]any)
		if parent[name] != nil && !ok {
			return nil, fmt.Errorf("%s isn't an object", name)
		}
		if next == nil {
			next = map[string]any{}
			if len(set) > 0 {
				at, key := parent, name
				parent[name] = next
				made = append(made, func() { delete(at, key) })
			}
		}
		parent = next
	}
	last := path[len(path)-1]
	list, ok := parent[last].([]any)
	if parent[last] != nil && !ok {
		return nil, fmt.Errorf("%s isn't a list", last)
	}
	envOf := func(e any) string {
		m, _ := e.(map[string]any)
		s, _ := m["env"].(string)
		return s
	}
	setNames := map[string]bool{}
	for _, v := range set {
		s, _ := v.(string)
		name, ref, ok := strings.Cut(s, "=")
		id, key, ok2 := strings.Cut(ref, ":")
		if !ok || !ok2 || name == "" || id == "" || key == "" {
			return nil, fmt.Errorf("--secret-env %q: want NAME=SECRET_ID:KEY", s)
		}
		if setNames[name] {
			return nil, fmt.Errorf("--secret-env: %s is given twice", name)
		}
		setNames[name] = true
		entry := map[string]any{"env": name, "secret": id, "key": key}
		if i := slices.IndexFunc(list, func(e any) bool { return envOf(e) == name }); i >= 0 {
			list[i] = entry
		} else {
			list = append(list, entry)
		}
	}
	// A name that isn't set is an error, not a no-op: a mistyped variable
	// shouldn't pass silently.
	for _, v := range remove {
		name, _ := v.(string)
		if setNames[name] {
			return nil, fmt.Errorf("--remove-secret-env %s: also given to --secret-env", name)
		}
		i := slices.IndexFunc(list, func(e any) bool { return envOf(e) == name })
		if i < 0 {
			return nil, fmt.Errorf("--remove-secret-env %s: no such secret variable", name)
		}
		list = slices.Delete(list, i, i+1)
	}
	if len(list) > 0 {
		parent[last] = list
		return doc, nil
	}
	delete(parent, last)
	for i := len(made) - 1; i >= 0; i-- {
		made[i]()
	}
	return doc, nil
}

// deepCopy copies a decoded JSON document, so an edit never changes the
// caller's. Values other than objects and lists are immutable and shared.
func deepCopy(doc map[string]any) map[string]any {
	if doc == nil {
		return map[string]any{}
	}
	return copyValue(doc).(map[string]any)
}

func copyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = copyValue(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = copyValue(e)
		}
		return out
	default:
		return v
	}
}
