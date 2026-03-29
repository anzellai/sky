func Lib_Money_CurrencySymbol(currency any) any {
	return func() any { return func() any { __subject := currency; if sky_asString(__subject) == "GBP" { return "£" };  if sky_asString(__subject) == "HKD" { return "HK$" };  if sky_asString(__subject) == "USD" { return "$" };  if true { return sky_concat(currency, " ") };  return nil }() }()
}

func Lib_Money_FormatAmount(amountInPence any) any {
	return func() any { pounds := sky_asInt(amountInPence) / sky_asInt(100); _ = pounds; pence := sky_modBy(100, amountInPence); _ = pence; penceStr := func() any { if sky_asBool(sky_asInt(pence) < sky_asInt(10)) { return sky_concat("0", sky_stringFromInt(pence)) }; return sky_stringFromInt(pence) }(); _ = penceStr; return sky_concat(sky_stringFromInt(pounds), sky_concat(".", penceStr)) }()
}

func Lib_Money_FormatPrice(amount any, currency any) any {
	return sky_concat(Lib_Money_CurrencySymbol(currency), Lib_Money_FormatAmount(amount))
}

func Lib_Money_FormatPriceFromRow(row any) any {
	return func() any { amount := Lib_Db_GetInt("price_amount", row); _ = amount; discount := Lib_Db_GetInt("price_discount", row); _ = discount; currency := Lib_Db_GetField("price_currency", row); _ = currency; effectivePrice := func() any { if sky_asBool(sky_asInt(discount) > sky_asInt(0)) { return sky_asInt(amount) - sky_asInt(discount) }; return amount }(); _ = effectivePrice; return func() any { if sky_asBool(sky_asInt(discount) > sky_asInt(0)) { return map[string]any{"original": sky_concat(Lib_Money_CurrencySymbol(currency), Lib_Money_FormatAmount(amount)), "discounted": sky_concat(Lib_Money_CurrencySymbol(currency), Lib_Money_FormatAmount(effectivePrice)), "hasDiscount": true} }; return map[string]any{"original": sky_concat(Lib_Money_CurrencySymbol(currency), Lib_Money_FormatAmount(amount)), "discounted": "", "hasDiscount": false} }() }()
}