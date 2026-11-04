package rt

// Only lower's resolved proof constructs this marker. Prepared provenance
// never grants the independent closed/pure capability on its own.
type SpaPreparedEncoderProof struct {
	Full      any
	InputType any
	Closed    bool
}

// Exact source callbacks support strict two-phase persistence. Full remains
// the reference for unsupported or ABI-normalized inputs.
type SpaPreparedModelEncoder struct {
	Full      any
	InputType any
	Prepare   any
	Finish    any
}

func (e SpaPreparedModelEncoder) encode(model any) (out string) {
	// Match spaEncodeModel's independent per-logical-invocation recovery.
	// A failed next call must not suppress the later previous call.
	defer func() {
		if recover() != nil {
			out = ""
		}
	}()
	prepared := sky_call(e.Prepare, struct{}{})
	return AsString(sky_call2(e.Finish, prepared, model))
}

// Private compiler plumbing, not a public application codec API.
func Spa_withPreparedModelEncoder(prepare, finish, encoder, cfg any) any {
	proven, ok := encoder.(SpaPreparedEncoderProof)
	if !ok {
		// Forged helpers/flags leave the original Full reference path intact.
		return Spa_withModelEncoder(encoder, cfg)
	}
	prepared := SpaPreparedModelEncoder{Full: proven.Full, InputType: proven.InputType, Prepare: prepare, Finish: finish}
	if proven.Closed {
		// Preserve current validated closed-model performance unchanged.
		cfg = Spa_withGeneratedModelEncoder(SpaAutoModelEncoder{Full: proven.Full, InputType: proven.InputType}, cfg)
	} else {
		cfg = Spa_withModelEncoder(proven.Full, cfg)
	}
	cfg = spaCfgSet(cfg, "GeneratedModelInputType", proven.InputType)
	return spaCfgSet(cfg, "PreparedAutoModelEncoder", prepared)
}
