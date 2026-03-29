func Lib_Products_ListProducts() any {
	return sky_call(sky_resultWithDefault([]any{}), Lib_Db_QueryWhere("products", "published", "==", true))
}

func Lib_Products_ListAllProducts() any {
	return func() any { result := Lib_Db_QueryDocs("products"); _ = result; func() any { return func() any { __subject := result; if sky_asSkyResult(__subject).SkyName == "Ok" { products := sky_asSkyResult(__subject).OkValue; _ = products; return sky_println(sky_concat("[PRODUCTS] Loaded ", sky_concat(sky_stringFromInt(sky_listLength(products)), " products"))) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return sky_println(sky_concat("[PRODUCTS] ERROR loading products: ", e)) };  return nil }() }(); return sky_call(sky_resultWithDefault([]any{}), result) }()
}

func Lib_Products_GetProduct(productId any) any {
	return func() any { return func() any { __subject := Lib_Db_GetDoc("products", productId); if sky_asSkyResult(__subject).SkyName == "Ok" { maybeProduct := sky_asSkyResult(__subject).OkValue; _ = maybeProduct; return maybeProduct };  if sky_asSkyResult(__subject).SkyName == "Err" { return SkyNothing() };  return nil }() }()
}

func Lib_Products_SearchProducts(query any, category any, sortBy any, sortDir any) any {
	return func() any { allProducts := Lib_Products_ListProducts(); _ = allProducts; filtered := sky_call(func(__pa0 any) any { return Lib_Products_SortProducts(sortBy, sortDir, __pa0) }, sky_call(func(__pa0 any) any { return Lib_Products_FilterBySearch(query, __pa0) }, sky_call(func(__pa0 any) any { return Lib_Products_FilterByCategory(category, __pa0) }, allProducts))); _ = filtered; return filtered }()
}

func Lib_Products_FilterByCategory(category any, products any) any {
	return func() any { if sky_asBool(sky_asBool(sky_equal(category, "all")) || sky_asBool(sky_equal(category, ""))) { return products }; return sky_call(sky_listFilter(func(p any) any { return sky_equal(Lib_Db_GetField("category", p), category) }), products) }()
}

func Lib_Products_FilterBySearch(query any, products any) any {
	return func() any { if sky_asBool(sky_equal(query, "")) { return products }; return func() any { lowerQuery := sky_stringToLower(query); _ = lowerQuery; return sky_call(sky_listFilter(func(p any) any { return sky_asBool(sky_call(sky_stringContains(lowerQuery), sky_stringToLower(Lib_Db_GetField("title", p)))) || sky_asBool(sky_call(sky_stringContains(lowerQuery), sky_stringToLower(Lib_Db_GetField("summary", p)))) }), products) }() }()
}

func Lib_Products_SortProducts(sortBy any, sortDir any, products any) any {
	return func() any { sorted := func() any { if sky_asBool(sky_equal(sortBy, "price")) { return sky_call(sky_listSortBy(func(p any) any { return sky_asInt(Lib_Db_GetInt("price_amount", p)) - sky_asInt(Lib_Db_GetInt("price_discount", p)) }), products) }; if sky_asBool(sky_equal(sortBy, "title")) { return sky_call(sky_listSortBy(func(_ any) any { return 0 }), products) }; return products }(); _ = sorted; return func() any { if sky_asBool(sky_equal(sortDir, "desc")) { return sky_listReverse(sorted) }; return sorted }() }()
}

func Lib_Products_CreateProduct(title any, summary any, category any, priceAmount any, priceCurrency any, priceDiscount any, stock any, published any) any {
	return func() any { productId := sky_call(sky_resultWithDefault(""), Github_Com_Google_Uuid_NewString(struct{}{})); _ = productId; productData := sky_dictFromList([]any{SkyTuple2{V0: "id", V1: productId}, SkyTuple2{V0: "title", V1: title}, SkyTuple2{V0: "summary", V1: summary}, SkyTuple2{V0: "category", V1: category}, SkyTuple2{V0: "price_amount", V1: Lib_Db_IntVal(priceAmount)}, SkyTuple2{V0: "price_currency", V1: priceCurrency}, SkyTuple2{V0: "price_discount", V1: Lib_Db_IntVal(priceDiscount)}, SkyTuple2{V0: "price_tax", V1: Lib_Db_IntVal(0)}, SkyTuple2{V0: "stock", V1: Lib_Db_IntVal(stock)}, SkyTuple2{V0: "published", V1: Lib_Db_BoolVal(published)}}); _ = productData; result := Lib_Db_SetDoc("products", productId, productData); _ = result; return func() any { return func() any { __subject := result; if sky_asSkyResult(__subject).SkyName == "Ok" { return func() any { sky_println(sky_concat("[PRODUCT] Created: ", title)); return SkyOk(productId) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }() }()
}

func Lib_Products_UpdateProduct(productId any, title any, summary any, category any, priceAmount any, priceCurrency any, priceDiscount any, stock any, published any) any {
	return func() any { productData := sky_dictFromList([]any{SkyTuple2{V0: "id", V1: productId}, SkyTuple2{V0: "title", V1: title}, SkyTuple2{V0: "summary", V1: summary}, SkyTuple2{V0: "category", V1: category}, SkyTuple2{V0: "price_amount", V1: Lib_Db_IntVal(priceAmount)}, SkyTuple2{V0: "price_currency", V1: priceCurrency}, SkyTuple2{V0: "price_discount", V1: Lib_Db_IntVal(priceDiscount)}, SkyTuple2{V0: "stock", V1: Lib_Db_IntVal(stock)}, SkyTuple2{V0: "published", V1: Lib_Db_BoolVal(published)}}); _ = productData; result := Lib_Db_SetDoc("products", productId, productData); _ = result; return func() any { return func() any { __subject := result; if sky_asSkyResult(__subject).SkyName == "Ok" { return func() any { sky_println(sky_concat("[PRODUCT] Updated: ", productId)); return SkyOk(productId) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }() }()
}

func Lib_Products_DeleteProduct(productId any) any {
	return func() any { images := Lib_Products_ListProductImages(productId); _ = images; sky_call(sky_listMap(func(img any) any { return Lib_Db_DeleteDoc("product_images", Lib_Db_GetField("id", img)) }), images); cartItems := sky_call(sky_resultWithDefault([]any{}), Lib_Db_QueryWhere("cart_items", "product_id", "==", productId)); _ = cartItems; sky_call(sky_listMap(func(item any) any { return Lib_Db_DeleteDoc("cart_items", Lib_Db_GetField("id", item)) }), cartItems); result := Lib_Db_DeleteDoc("products", productId); _ = result; sky_println(sky_concat("[PRODUCT] Deleted: ", productId)); return func() any { return func() any { __subject := result; if sky_asSkyResult(__subject).SkyName == "Ok" { v := sky_asSkyResult(__subject).OkValue; _ = v; return SkyOk(v) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }() }()
}

func Lib_Products_ListProductImages(productId any) any {
	return sky_call(sky_resultWithDefault([]any{}), Lib_Db_QueryWhere("product_images", "product_id", "==", productId))
}

func Lib_Products_AddProductImage(productId any, imageData any) any {
	return func() any { imageId := sky_call(sky_resultWithDefault(""), Github_Com_Google_Uuid_NewString(struct{}{})); _ = imageId; existingImages := Lib_Products_ListProductImages(productId); _ = existingImages; nextPosition := sky_stringFromInt(sky_listLength(existingImages)); _ = nextPosition; imageDoc := sky_dictFromList([]any{SkyTuple2{V0: "id", V1: imageId}, SkyTuple2{V0: "product_id", V1: productId}, SkyTuple2{V0: "data", V1: imageData}, SkyTuple2{V0: "position", V1: nextPosition}}); _ = imageDoc; result := Lib_Db_SetDoc("product_images", imageId, imageDoc); _ = result; return func() any { return func() any { __subject := result; if sky_asSkyResult(__subject).SkyName == "Ok" { return func() any { sky_println(sky_concat("[IMAGE] Added to product: ", productId)); return SkyOk(imageId) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }() }()
}

func Lib_Products_DeleteProductImage(imageId any) any {
	return func() any { result := Lib_Db_DeleteDoc("product_images", imageId); _ = result; sky_println(sky_concat("[IMAGE] Deleted: ", imageId)); return func() any { return func() any { __subject := result; if sky_asSkyResult(__subject).SkyName == "Ok" { v := sky_asSkyResult(__subject).OkValue; _ = v; return SkyOk(v) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }() }()
}