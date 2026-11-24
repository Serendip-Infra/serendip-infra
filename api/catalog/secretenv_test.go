package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSecretEnvEdits(t *testing.T) {
	doc := func() map[string]any {
		return map[string]any{"metadata": map[string]any{"name": "web", "resourceVersion": "3"},
			"spec": map[string]any{"image": "nginx", "secretEnv": []any{
				map[string]any{"env": "A", "secret": "s1", "key": "a"},
				map[string]any{"env": "B", "secret": "s1", "key": "b"},
			}}}
	}
	for _, tt := range []struct {
		name, op    string
		set, remove []any
		want        string // the edited list, as JSON; "" for no list
		err         string
	}{
		{name: "add", op: "components_update", set: []any{"C=s2:k:with:colons"},
			want: `[{"env":"A","key":"a","secret":"s1"},{"env":"B","key":"b","secret":"s1"},{"env":"C","key":"k:with:colons","secret":"s2"}]`},
		{name: "replace in place", op: "components_update", set: []any{"A=s3:x"},
			want: `[{"env":"A","key":"x","secret":"s3"},{"env":"B","key":"b","secret":"s1"}]`},
		{name: "remove", op: "components_create", remove: []any{"A"},
			want: `[{"env":"B","key":"b","secret":"s1"}]`},
		{name: "remove all drops the list", op: "components_update", remove: []any{"A", "B"}},
		{name: "remove missing", op: "components_update", remove: []any{"Z"}, err: "no such secret variable"},
		{name: "set and remove one name", op: "components_update", set: []any{"A=s1:a"}, remove: []any{"A"}, err: "also given"},
		{name: "set twice", op: "components_update", set: []any{"C=s1:a", "C=s1:b"}, err: "given twice"},
		{name: "no key", op: "components_update", set: []any{"C=s1"}, err: "NAME=SECRET_ID:KEY"},
		{name: "no name", op: "components_update", set: []any{"=s1:a"}, err: "NAME=SECRET_ID:KEY"},
		{name: "empty key", op: "components_update", set: []any{"C=s1:"}, err: "NAME=SECRET_ID:KEY"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			op, _ := Lookup(tt.op)
			in := doc()
			args := map[string]any{"resource": in}
			if tt.op == "components_update" {
				args["id"] = "c1"
			}
			if tt.set != nil {
				args["secret-env"] = tt.set
			}
			if tt.remove != nil {
				args["remove-secret-env"] = tt.remove
			}
			req, err := op.Request(args)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatal(err)
			}
			spec := body["spec"].(map[string]any)
			got := ""
			if l, ok := spec["secretEnv"]; ok {
				b, _ := json.Marshal(l)
				got = string(b)
			}
			if got != tt.want {
				t.Errorf("list %s, want %s", got, tt.want)
			}
			if spec["image"] != "nginx" || body["metadata"].(map[string]any)["resourceVersion"] != "3" {
				t.Errorf("the rest of the document changed: %s", req.Body)
			}
			if b, _ := json.Marshal(in); string(b) != func() string { b, _ := json.Marshal(doc()); return string(b) }() {
				t.Errorf("the caller's document changed: %s", b)
			}
		})
	}
}

func TestDeploymentSecretEnvGoesInOverrides(t *testing.T) {
	op, _ := Lookup("deployments_create")
	req, err := op.Request(map[string]any{
		"resource":   map[string]any{"metadata": map[string]any{"name": "web-dev"}, "spec": map[string]any{"componentId": "c1"}},
		"secret-env": []any{"DB_PASSWORD=s1:password"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"metadata":{"name":"web-dev"},"spec":{"componentId":"c1","overrides":{"secretEnv":[{"env":"DB_PASSWORD","key":"password","secret":"s1"}]}}}`
	if string(req.Body) != want {
		t.Errorf("body %s, want %s", req.Body, want)
	}
	// Without the flags, the document goes as written.
	req, _ = op.Request(map[string]any{"resource": map[string]any{"spec": map[string]any{"componentId": "c1"}}})
	if string(req.Body) != `{"spec":{"componentId":"c1"}}` {
		t.Errorf("unedited body %s", req.Body)
	}
}

func TestSecretEnvFlagsAreCLIOnly(t *testing.T) {
	for _, name := range []string{"components_create", "components_update", "deployments_create", "deployments_update"} {
		op, _ := Lookup(name)
		props := op.ForMCP().InputSchema()["properties"].(map[string]any)
		if _, ok := props["secret-env"]; ok {
			t.Errorf("%s: the MCP tool takes secret-env", name)
		}
		if props["resource"] == nil {
			t.Errorf("%s: the MCP tool lost its resource", name)
		}
		if s := op.InputSchema()["properties"].(map[string]any)["secret-env"].(map[string]any); s["type"] != "array" {
			t.Errorf("%s: secret-env schema %v", name, s)
		}
	}
	if _, err := mustLookup(t, "components_update").Request(map[string]any{"id": "c1", "resource": map[string]any{}, "secret-env": "A=s:k"}); err == nil {
		t.Error("a single string passed as a list")
	}
}

func TestSecretEnvEditEdges(t *testing.T) {
	op := mustLookup(t, "deployments_update")
	// An edit without a document is refused, never sent unedited.
	if _, err := op.Request(map[string]any{"id": "d1", "secret-env": []any{"A=s:k"}}); err == nil {
		t.Error("an edit with no document passed")
	}
	if _, err := op.Request(map[string]any{"id": "d1", "resource": map[string]any{"spec": "x"}, "secret-env": []any{"A=s:k"}}); err == nil {
		t.Error("an edit of a spec that isn't an object passed")
	}
	// Removing the last variable leaves no empty overrides behind, and a
	// remove never adds one.
	for _, doc := range []map[string]any{
		{"spec": map[string]any{"overrides": map[string]any{"secretEnv": []any{map[string]any{"env": "A", "secret": "s", "key": "k"}}}}},
	} {
		req, err := op.Request(map[string]any{"id": "d1", "resource": doc, "remove-secret-env": []any{"A"}})
		if err != nil || string(req.Body) != `{"spec":{"overrides":{}}}` {
			t.Errorf("remove the last: %s %v", req.Body, err)
		}
	}
	if _, err := op.Request(map[string]any{"id": "d1", "resource": map[string]any{"spec": map[string]any{}}, "remove-secret-env": []any{"A"}}); err == nil {
		t.Error("removing from no list passed")
	}
	// Numbers are copied as they are, not through a float.
	req, err := op.Request(map[string]any{"id": "d1", "resource": map[string]any{"spec": map[string]any{"replicas": json.Number("9007199254740993")}},
		"secret-env": []any{"A=s:k"}})
	if err != nil || !strings.Contains(string(req.Body), `"replicas":9007199254740993`) {
		t.Errorf("number: %s %v", req.Body, err)
	}
}
