package model

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestReadable(t *testing.T) {
	value := map[string]any{
		"name":    "🚀 Proxy",
		"members": []string{"♻️ Auto", "🕹️ miHoYo", "Ⓜ️ Microsoft", `say "\U0001F680"`, "plain"},
		"rule":    "MATCH,🐟 Others",
	}
	encoded, err := yaml.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	readable := Readable(encoded)
	var back map[string]any
	if err := yaml.Unmarshal(readable, &back); err != nil {
		t.Fatalf("%v\n%s", err, readable)
	}
	want, _ := yaml.Marshal(value)
	got, _ := yaml.Marshal(back)
	if string(got) != string(want) {
		t.Errorf("round trip changed:\n%s\nfrom\n%s", readable, encoded)
	}
	for _, name := range []string{"🚀 Proxy", "🕹️ miHoYo", "🐟 Others"} {
		if !strings.Contains(string(readable), name) {
			t.Errorf("%q is not written as itself:\n%s", name, readable)
		}
	}
}

// Text like an escape outside a double-quoted scalar stays as written: Readable finds the
// scalars by parsing, not by the characters around a quote.
func TestReadableOnlyQuoted(t *testing.T) {
	long := strings.Repeat("🚀 word ", 30)
	values := []any{
		New(
			"name", `a, "b`,
			"password", `x\U0001F600y`,
			"other", `a - "x\U0001F600y`,
			"block", "line one\n\"\\U0001F600\"\n",
			"single", `'\U0001F600`,
			"🚀 key", "🚀 value",
			"escaped", `🚀 \U0001F600 "quoted"`,
			"breaks", "a\u2028b\u0085c",
			"after", "🐟 after the breaks",
			"long", long,
			"tag", "\U000E0001 \U000F0000 🚀",
		),
		[]any{New("members", []any{"♻️ Auto", "🇭🇰 Hong Kong", `x\U0001F600y`})},
	}
	for _, value := range values {
		for _, flow := range []bool{false, true} {
			var node yaml.Node
			if err := node.Encode(value); err != nil {
				t.Fatal(err)
			}
			if flow {
				setFlow(&node)
			}
			encoded, err := yaml.Marshal(&node)
			if err != nil {
				t.Fatal(err)
			}
			// Two documents, as a stream may have.
			encoded = append(append(encoded, "---\n"...), encoded...)
			readable := Readable(encoded)
			want, got := documents(t, encoded), documents(t, readable)
			if got != want {
				t.Errorf("read back as\n%s\nnot\n%s\nfrom\n%s", got, want, readable)
			}
			for _, text := range []string{`x\U0001F600y`, "🇭🇰 Hong Kong", `\U000E0001`, `\U000F0000`} {
				if bytes.Contains(encoded, []byte(text)) && !bytes.Contains(readable, []byte(text)) {
					t.Errorf("%q is gone from\n%s", text, readable)
				}
			}
			if bytes.Contains(readable, []byte(`\U0001F680`)) || bytes.Contains(readable, []byte(`\U0001F41F`)) {
				t.Errorf("an emoji is still escaped:\n%s", readable)
			}
		}
	}
}

func setFlow(node *yaml.Node) {
	if node.Kind == yaml.MappingNode || node.Kind == yaml.SequenceNode {
		node.Style = yaml.FlowStyle
	}
	for _, child := range node.Content {
		setFlow(child)
	}
}

func documents(t *testing.T, text []byte) string {
	t.Helper()
	decoder := yaml.NewDecoder(bytes.NewReader(text))
	var out []string
	for {
		var value any
		if err := decoder.Decode(&value); err != nil {
			if err.Error() != "EOF" {
				t.Fatalf("%v\n%s", err, text)
			}
			return strings.Join(out, "\n---\n")
		}
		out = append(out, fmt.Sprintf("%#v", value))
	}
}

func TestSameAstral(t *testing.T) {
	for _, c := range []struct {
		written, value string
		same           bool
	}{
		{`"🚀 a 🐟"`, "🚀 a 🐟", true},
		{`"🐟"`, "🚀", false},
		{`"🚀"`, "🚀 🐟", false},
		{`"\U000E0001 🚀"`, "\U000E0001 🚀", true}, // not graphic: stays escaped
		{`"plain"`, "plain", true},
	} {
		if same := sameAstral([]byte(c.written), c.value); same != c.same {
			t.Errorf("sameAstral(%s, %q) = %v", c.written, c.value, same)
		}
	}
}
