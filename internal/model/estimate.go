package model

// PricePerMTok returns the built-in or configured USD price per million
// input and output tokens for spec under DefaultRuntime. ok is false when
// the model is unpriced.
func PricePerMTok(spec string) (in, out float64, ok bool) {
	return DefaultRuntime.PricePerMTok(spec)
}

// PricePerMTok returns the built-in or configured USD price per million
// input and output tokens for spec. ok is false when the model is unpriced.
func (r *Runtime) PricePerMTok(spec string) (in, out float64, ok bool) {
	if r.Unpriced(spec) {
		return 0, 0, false
	}
	_, name := splitSpec(spec)
	custom := r.providerPrice(spec)
	in = r.cachedPriceWith(name, custom, 1_000_000, 0, 0, 0).USD
	out = r.cachedPriceWith(name, custom, 0, 0, 0, 1_000_000).USD
	return in, out, true
}
