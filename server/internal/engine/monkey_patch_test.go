package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMonkeyPatch(t *testing.T) {
	engine := New(4)
	sess, buf := newSession(t, nil, 100)

	code := `

def fake_parser(text):
    return {"foo": "bar"}

test.monkey_patch(target = url.parse, handler = fake_parser)
print(url.parse("random"))
	`

	res := engine.Exec(
		t.Context(), sess, "1", code, time.Hour, 1000, func() string { return buf.String() },
	)

	assert.Nil(t, res.Error)
	assert.Equal(t, `{"foo": "bar"}
`, res.Output)
}

func TestMonkeyPatchCapability(t *testing.T) {
	engine := New(4)
	sess, buf := newSession(t, nil, 1024)

	code := `
def fake_read(path):
    return "mocked:" + path

test.monkey_patch(target = fs.read, handler = fake_read)
print(fs.read("/missing.txt"))
`

	res := engine.Exec(
		t.Context(), sess, "cap", code, time.Hour, 1000, func() string { return buf.String() },
	)

	assert.Nil(t, res.Error)
	assert.Equal(t, "mocked:/missing.txt\n", res.Output)
}
