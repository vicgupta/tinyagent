package model

import "testing"

func TestPricePerMTok(t *testing.T) {
	in, out, ok := PricePerMTok("openai/gpt-4o")
	p, _ := lookupPrice("gpt-4o")
	if !ok || in != p[0] || out != p[1] {
		t.Errorf("gpt-4o = %v, %v, %v; table %v", in, out, ok, p)
	}
	if _, _, ok := PricePerMTok("openai/no-such-model-xyz"); ok {
		t.Error("unpriced model reported as priced")
	}
}
