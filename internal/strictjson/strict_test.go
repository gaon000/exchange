package strictjson_test

import (
	"encoding/json"
	"strings"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/strictjson"
)

type input struct {
	Name     string `json:"name"`
	Count    d.Int  `json:"count"`
	Items    []item `json:"items"`
	Optional string `json:"optional,omitempty"`
	Ptr      *item  `json:"ptr,omitempty"`
}
type item struct {
	ID string `json:"id"`
}

func TestDecodeStrictShape(t *testing.T) {
	valid := `{"name":"alice","count":"00012","items":[{"id":"x"}]}`
	var got input
	if err := strictjson.Decode([]byte(valid), &got); err != nil || got.Count != 12 || len(got.Items) != 1 {
		t.Fatalf("%+v: %v", got, err)
	}
	for _, data := range []string{
		`null`, `[]`, `{}`, valid + ` {}`, valid + `!`,
		strings.Replace(valid, `"name"`, `"Name"`, 1),
		strings.Replace(valid, `"name":"alice"`, `"name":"alice","name":"bob"`, 1),
		strings.Replace(valid, `"id":"x"`, `"id":"x","\u0069d":"y"`, 1),
		strings.Replace(valid, `"id":"x"`, `"ID":"x"`, 1),
		strings.Replace(valid, `"id":"x"`, `"id":"x","extra":1`, 1),
		strings.Replace(valid, `"alice"`, `null`, 1),
		strings.Replace(valid, `"00012"`, `12`, 1),
		strings.Replace(valid, `"00012"`, `"9223372036854775808"`, 1),
		strings.Replace(valid, `"00012"`, `null`, 1),
		strings.Replace(valid, `[{"id":"x"}]`, `null`, 1),
		strings.Replace(valid, `[{"id":"x"}]`, `[null]`, 1),
		strings.Replace(valid, `"alice"`, "\"\xff\"", 1),
		strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66),
	} {
		t.Run(data, func(t *testing.T) {
			if err := strictjson.Decode([]byte(data), &input{}); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
	if err := strictjson.Decode([]byte(`{"name":"a","count":"0","items":[],"ptr":null}`), &got); err != nil {
		t.Fatal(err)
	}
}

func TestRawPayloadStillRejectsDuplicates(t *testing.T) {
	var got struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := strictjson.Decode([]byte(`{"payload":{"x":1,"x":2}}`), &got); err == nil {
		t.Fatal("duplicate inside raw payload accepted")
	}
	for _, dst := range []any{nil, (*input)(nil), input{}} {
		if err := strictjson.Decode([]byte(`{}`), dst); err == nil {
			t.Fatal("invalid destination")
		}
	}
}
