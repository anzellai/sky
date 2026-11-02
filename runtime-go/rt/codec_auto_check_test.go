package rt

import "testing"

type acPartialInner struct {
	Amounts []SkyADT `sky:"amounts,[]Std_Money_Money"`
}

type acPartial struct {
	Id    string             `sky:"id,string"`
	Price SkyADT             `sky:"price,Std_Decimal_Decimal"`
	Tip   SkyMaybe[SkyADT]   `sky:"tip,rt.SkyMaybe[Std_Decimal_Decimal]"`
	Nest  acPartialInner     `sky:"nest,Main_Inner_R"`
	Qty   int                `sky:"qty,int"`
	Tags  []string           `sky:"tags,[]string"`
	Opt   SkyMaybe[string]   `sky:"opt,rt.SkyMaybe[string]"`
	Plain acPartialPlainNest `sky:"plain,Main_Plain_R"`
}

type acPartialPlainNest struct {
	A int `sky:"a,int"`
}

// v0.29.2 fifth Judge: a Codec.auto field whose type holds a Decimal or a
// Money (at any depth) is partial: a computed Decimal can be longer than the
// strict reader takes. Codec_autoCols marks exactly those columns `|check`,
// and Std.Codec checks them on a write; every other column is unmarked.
func TestCodecAutoColsMarksPartialColumns(t *testing.T) {
	got := map[string]string{}
	for _, c := range AsList(Codec_autoCols(true, acPartial{})) {
		p := c.(T2[any, any])
		got[p.V0.(string)] = p.V1.(string)
	}
	for col, partial := range map[string]bool{"id": false, "price": true, "tip": true, "nest": true, "qty": false, "tags": false, "opt": false, "plain": false} {
		marked := len(got[col]) > 6 && got[col][len(got[col])-6:] == "|check"
		if marked != partial {
			t.Errorf("column %s kind %q: marked %v, want %v", col, got[col], marked, partial)
		}
	}
}
