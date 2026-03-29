func Lib_Cart_GetOrCreateCart(userId any) any {
	return func() any { cartId := sky_concat(userId, "-cart"); _ = cartId; return func() any { return func() any { __subject := Lib_Db_GetDoc("carts", cartId); if sky_asSkyResult(__subject).SkyName == "Ok" && sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).SkyName == "Just" { cart := sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).JustValue; _ = cart; return func() any { if sky_asBool(sky_equal(Lib_Db_GetField("state", cart), "ongoing")) { return SkyOk(cart) }; return Lib_Cart_CreateCart(userId) }() };  if sky_asSkyResult(__subject).SkyName == "Ok" && sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).SkyName == "Nothing" { return Lib_Cart_CreateCart(userId) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }() }()
}

func Lib_Cart_CreateCart(userId any) any {
	return func() any { cartId := sky_concat(userId, "-cart"); _ = cartId; cartData := sky_dictFromList([]any{SkyTuple2{V0: "id", V1: cartId}, SkyTuple2{V0: "owner_id", V1: userId}, SkyTuple2{V0: "state", V1: "ongoing"}, SkyTuple2{V0: "checkout_link", V1: ""}, SkyTuple2{V0: "checkout_reference", V1: ""}, SkyTuple2{V0: "checkout_remarks", V1: ""}, SkyTuple2{V0: "shipping_name", V1: ""}, SkyTuple2{V0: "shipping_email", V1: ""}, SkyTuple2{V0: "shipping_phone", V1: ""}, SkyTuple2{V0: "shipping_line1", V1: ""}, SkyTuple2{V0: "shipping_line2", V1: ""}, SkyTuple2{V0: "shipping_city", V1: ""}, SkyTuple2{V0: "shipping_country", V1: ""}, SkyTuple2{V0: "shipping_postcode", V1: ""}, SkyTuple2{V0: "total_amount", V1: Lib_Db_IntVal(0)}, SkyTuple2{V0: "total_currency", V1: "GBP"}}); _ = cartData; return func() any { return func() any { __subject := Lib_Db_SetDoc("carts", cartId, cartData); if sky_asSkyResult(__subject).SkyName == "Ok" { return SkyOk(cartData) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }() }()
}

func Lib_Cart_GetCart(cartId any) any {
	return func() any { return func() any { __subject := Lib_Db_GetDoc("carts", cartId); if sky_asSkyResult(__subject).SkyName == "Ok" { maybeCart := sky_asSkyResult(__subject).OkValue; _ = maybeCart; return maybeCart };  if sky_asSkyResult(__subject).SkyName == "Err" { return SkyNothing() };  return nil }() }()
}

func Lib_Cart_GetCartItems(cartId any) any {
	return func() any { itemsResult := Lib_Db_QueryWhere("cart_items", "cart_id", "==", cartId); _ = itemsResult; items := sky_call(sky_resultWithDefault([]any{}), itemsResult); _ = items; return sky_call(sky_listMap(Lib_Cart_EnrichCartItem), items) }()
}

func Lib_Cart_EnrichCartItem(item any) any {
	return func() any { productId := Lib_Db_GetField("product_id", item); _ = productId; return func() any { return func() any { __subject := Lib_Db_GetDoc("products", productId); if sky_asSkyResult(__subject).SkyName == "Ok" && sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).SkyName == "Just" { product := sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).JustValue; _ = product; return sky_call2(sky_dictInsert("title"), Lib_Db_GetField("title", product), sky_call2(sky_dictInsert("summary"), Lib_Db_GetField("summary", product), sky_call2(sky_dictInsert("price_amount"), Lib_Db_GetField("price_amount", product), sky_call2(sky_dictInsert("price_currency"), Lib_Db_GetField("price_currency", product), sky_call2(sky_dictInsert("price_discount"), Lib_Db_GetField("price_discount", product), sky_call2(sky_dictInsert("price_tax"), Lib_Db_GetField("price_tax", product), sky_call2(sky_dictInsert("stock"), Lib_Db_GetField("stock", product), item))))))) };  if true { return item };  return nil }() }() }()
}

func Lib_Cart_GetCartWithItems(cartId any) any {
	return func() any { cart := Lib_Cart_GetCart(cartId); _ = cart; items := func() any { return func() any { __subject := cart; if sky_asSkyMaybe(__subject).SkyName == "Just" { c := sky_asSkyMaybe(__subject).JustValue; _ = c; return Lib_Cart_GetCartItems(Lib_Db_GetField("id", c)) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return []any{} };  return nil }() }(); _ = items; return SkyTuple2{V0: cart, V1: items} }()
}

func Lib_Cart_AddItem(cartId any, productId any, quantity any) any {
	return func() any { existingItems := sky_call(sky_resultWithDefault([]any{}), Lib_Db_QueryWhere("cart_items", "cart_id", "==", cartId)); _ = existingItems; matchingItems := sky_call(sky_listFilter(func(item any) any { return sky_equal(Lib_Db_GetField("product_id", item), productId) }), existingItems); _ = matchingItems; return func() any { return func() any { __subject := sky_listHead(matchingItems); if sky_asSkyMaybe(__subject).SkyName == "Just" { existing := sky_asSkyMaybe(__subject).JustValue; _ = existing; return func() any { existingQty := Lib_Db_GetInt("quantity", existing); _ = existingQty; newQty := sky_asInt(existingQty) + sky_asInt(quantity); _ = newQty; itemId := Lib_Db_GetField("id", existing); _ = itemId; updatedItem := sky_call2(sky_dictInsert("quantity"), Lib_Db_IntVal(newQty), existing); _ = updatedItem; Lib_Db_SetDoc("cart_items", itemId, updatedItem); Lib_Cart_RecalculateTotal(cartId); return SkyOk(struct{}{}) }() };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return func() any { itemId := sky_call(sky_resultWithDefault(""), Github_Com_Google_Uuid_NewString(struct{}{})); _ = itemId; itemData := sky_dictFromList([]any{SkyTuple2{V0: "id", V1: itemId}, SkyTuple2{V0: "cart_id", V1: cartId}, SkyTuple2{V0: "product_id", V1: productId}, SkyTuple2{V0: "quantity", V1: Lib_Db_IntVal(quantity)}}); _ = itemData; Lib_Db_SetDoc("cart_items", itemId, itemData); Lib_Cart_RecalculateTotal(cartId); return SkyOk(struct{}{}) }() };  return nil }() }() }()
}

func Lib_Cart_UpdateItemQty(itemId any, quantity any) any {
	return func() any { if sky_asBool(sky_asInt(quantity) <= sky_asInt(0)) { return Lib_Cart_RemoveItem(itemId) }; return func() any { return func() any { __subject := Lib_Db_GetDoc("cart_items", itemId); if sky_asSkyResult(__subject).SkyName == "Ok" && sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).SkyName == "Just" { item := sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).JustValue; _ = item; return func() any { updatedItem := sky_call2(sky_dictInsert("quantity"), Lib_Db_IntVal(quantity), item); _ = updatedItem; Lib_Db_SetDoc("cart_items", itemId, updatedItem); Lib_Cart_RecalculateTotal(Lib_Db_GetField("cart_id", item)); return SkyOk(struct{}{}) }() };  if true { return SkyOk(struct{}{}) };  return nil }() }() }()
}

func Lib_Cart_RemoveItem(itemId any) any {
	return func() any { return func() any { __subject := Lib_Db_GetDoc("cart_items", itemId); if sky_asSkyResult(__subject).SkyName == "Ok" && sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).SkyName == "Just" { item := sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).JustValue; _ = item; return func() any { cartId := Lib_Db_GetField("cart_id", item); _ = cartId; Lib_Db_DeleteDoc("cart_items", itemId); return func() any { if sky_asBool(!sky_equal(cartId, "")) { return func() any { Lib_Cart_RecalculateTotal(cartId); return SkyOk(struct{}{}) }() }; return SkyOk(struct{}{}) }() }() };  if true { return SkyOk(struct{}{}) };  return nil }() }()
}

func Lib_Cart_RecalculateTotal(cartId any) any {
	return func() any { items := Lib_Cart_GetCartItems(cartId); _ = items; total := sky_call2(sky_listFoldl(func(item any) any { return func(acc any) any { return func() any { price := Lib_Db_GetInt("price_amount", item); _ = price; discount := Lib_Db_GetInt("price_discount", item); _ = discount; qty := Lib_Db_GetInt("quantity", item); _ = qty; effectivePrice := sky_asInt(price) - sky_asInt(discount); _ = effectivePrice; itemTotal := func() any { if sky_asBool(sky_asInt(effectivePrice) > sky_asInt(0)) { return sky_asInt(effectivePrice) * sky_asInt(qty) }; return 0 }(); _ = itemTotal; return sky_asInt(acc) + sky_asInt(itemTotal) }() } }), 0, items); _ = total; currency := func() any { return func() any { __subject := sky_listHead(items); if sky_asSkyMaybe(__subject).SkyName == "Just" { item := sky_asSkyMaybe(__subject).JustValue; _ = item; return Lib_Db_GetField("price_currency", item) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "GBP" };  return nil }() }(); _ = currency; return func() any { return func() any { __subject := Lib_Db_GetDoc("carts", cartId); if sky_asSkyResult(__subject).SkyName == "Ok" && sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).SkyName == "Just" { cart := sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).JustValue; _ = cart; return func() any { updatedCart := sky_call2(sky_dictInsert("total_amount"), Lib_Db_IntVal(total), sky_call2(sky_dictInsert("total_currency"), currency, cart)); _ = updatedCart; Lib_Db_SetDoc("carts", cartId, updatedCart); return total }() };  if true { return total };  return nil }() }() }()
}

func Lib_Cart_FindDocAndCollection(docId any) any {
	return func() any { return func() any { __subject := Lib_Db_GetDoc("orders", docId); if sky_asSkyResult(__subject).SkyName == "Ok" && sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).SkyName == "Just" { doc := sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).JustValue; _ = doc; return SkyOk(SkyTuple2{V0: "orders", V1: doc}) };  if true { return func() any { return func() any { __subject := Lib_Db_GetDoc("carts", docId); if sky_asSkyResult(__subject).SkyName == "Ok" && sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).SkyName == "Just" { doc := sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).JustValue; _ = doc; return SkyOk(SkyTuple2{V0: "carts", V1: doc}) };  if sky_asSkyResult(__subject).SkyName == "Ok" && sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).SkyName == "Nothing" { return SkyErr("Document not found.") };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }() };  return nil }() }()
}

func Lib_Cart_SetCartState(docId any, newState any) any {
	return func() any { return func() any { __subject := Lib_Cart_FindDocAndCollection(docId); if sky_asSkyResult(__subject).SkyName == "Ok" { collection := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = collection; doc := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = doc; return func() any { updatedDoc := sky_call2(sky_dictInsert("state"), newState, doc); _ = updatedDoc; result := Lib_Db_SetDoc(collection, docId, updatedDoc); _ = result; sky_println(sky_concat("[ORDER] State changed: ", sky_concat(docId, sky_concat(" -> ", newState)))); return func() any { return func() any { __subject := result; if sky_asSkyResult(__subject).SkyName == "Ok" { v := sky_asSkyResult(__subject).OkValue; _ = v; return SkyOk(v) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }() }() };  return nil }() }()
}

func Lib_Cart_SetCheckout(docId any, link any, reference any, remarks any) any {
	return func() any { return func() any { __subject := Lib_Cart_FindDocAndCollection(docId); if sky_asSkyResult(__subject).SkyName == "Ok" { collection := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = collection; doc := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = doc; return func() any { updatedDoc := sky_call2(sky_dictInsert("checkout_link"), link, sky_call2(sky_dictInsert("checkout_reference"), reference, sky_call2(sky_dictInsert("checkout_remarks"), remarks, doc))); _ = updatedDoc; return Lib_Db_SetDoc(collection, docId, updatedDoc) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }()
}

func Lib_Cart_SetShipping(docId any, name any, email any, phone any, line1 any, line2 any, city any, country any, postcode any) any {
	return func() any { return func() any { __subject := Lib_Cart_FindDocAndCollection(docId); if sky_asSkyResult(__subject).SkyName == "Ok" { collection := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = collection; doc := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = doc; return func() any { updatedDoc := sky_call2(sky_dictInsert("shipping_name"), name, sky_call2(sky_dictInsert("shipping_email"), email, sky_call2(sky_dictInsert("shipping_phone"), phone, sky_call2(sky_dictInsert("shipping_line1"), line1, sky_call2(sky_dictInsert("shipping_line2"), line2, sky_call2(sky_dictInsert("shipping_city"), city, sky_call2(sky_dictInsert("shipping_country"), country, sky_call2(sky_dictInsert("shipping_postcode"), postcode, doc)))))))); _ = updatedDoc; return Lib_Db_SetDoc(collection, docId, updatedDoc) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }()
}

func Lib_Cart_CheckoutToOrder(cartId any) any {
	return func() any { orderId := sky_call(sky_resultWithDefault(""), Github_Com_Google_Uuid_NewString(struct{}{})); _ = orderId; return func() any { return func() any { __subject := Lib_Db_GetDoc("carts", cartId); if sky_asSkyResult(__subject).SkyName == "Ok" && sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).SkyName == "Just" { cart := sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).JustValue; _ = cart; return func() any { orderData := sky_call2(sky_dictInsert("id"), orderId, sky_call2(sky_dictInsert("cart_id"), cartId, cart)); _ = orderData; Lib_Db_SetDoc("orders", orderId, orderData); cartItems := sky_call(sky_resultWithDefault([]any{}), Lib_Db_QueryWhere("cart_items", "cart_id", "==", cartId)); _ = cartItems; sky_call(sky_listMap(func(item any) any { return func() any { itemId := sky_call(sky_resultWithDefault(""), Github_Com_Google_Uuid_NewString(struct{}{})); _ = itemId; enriched := Lib_Cart_EnrichCartItem(item); _ = enriched; orderItem := sky_call2(sky_dictInsert("id"), itemId, sky_call2(sky_dictInsert("order_id"), orderId, sky_call2(sky_dictInsert("cart_id"), cartId, enriched))); _ = orderItem; return Lib_Db_SetDoc("order_items", itemId, orderItem) }() }), cartItems); sky_println(sky_concat("[CHECKOUT] Created order ", sky_concat(orderId, sky_concat(" from cart ", cartId)))); return SkyOk(orderId) }() };  if true { return SkyErr("Cart not found.") };  return nil }() }() }()
}

func Lib_Cart_GetOrder(orderId any) any {
	return func() any { return func() any { __subject := Lib_Db_GetDoc("orders", orderId); if sky_asSkyResult(__subject).SkyName == "Ok" { maybeOrder := sky_asSkyResult(__subject).OkValue; _ = maybeOrder; return maybeOrder };  if sky_asSkyResult(__subject).SkyName == "Err" { return SkyNothing() };  return nil }() }()
}

func Lib_Cart_GetOrderItems(orderId any) any {
	return sky_call(sky_resultWithDefault([]any{}), Lib_Db_QueryWhere("order_items", "order_id", "==", orderId))
}

func Lib_Cart_GetOrderWithItems(orderId any) any {
	return func() any { order := Lib_Cart_GetOrder(orderId); _ = order; items := func() any { return func() any { __subject := order; if sky_asSkyMaybe(__subject).SkyName == "Just" { return Lib_Cart_GetOrderItems(orderId) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return []any{} };  return nil }() }(); _ = items; return SkyTuple2{V0: order, V1: items} }()
}

func Lib_Cart_GetUserCarts(userId any) any {
	return sky_call(sky_resultWithDefault([]any{}), Lib_Db_QueryWhere("orders", "owner_id", "==", userId))
}

func Lib_Cart_GetOrdersByState(state any) any {
	return func() any { if sky_asBool(sky_equal(state, "all")) { return Lib_Cart_GetAllOrders() }; return func() any { orders := sky_call(sky_resultWithDefault([]any{}), Lib_Db_QueryWhere("orders", "state", "==", state)); _ = orders; return sky_call(sky_listMap(Lib_Cart_EnrichOrderWithUser), orders) }() }()
}

func Lib_Cart_GetAllOrders() any {
	return func() any { allOrders := sky_call(sky_resultWithDefault([]any{}), Lib_Db_QueryDocs("orders")); _ = allOrders; return sky_call(sky_listMap(Lib_Cart_EnrichOrderWithUser), allOrders) }()
}

func Lib_Cart_EnrichOrderWithUser(order any) any {
	return func() any { ownerId := Lib_Db_GetField("owner_id", order); _ = ownerId; return func() any { return func() any { __subject := Lib_Db_GetDoc("users", ownerId); if sky_asSkyResult(__subject).SkyName == "Ok" && sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).SkyName == "Just" { user := sky_asSkyMaybe(sky_asSkyResult(__subject).OkValue).JustValue; _ = user; return sky_call2(sky_dictInsert("user_name"), Lib_Db_GetField("name", user), sky_call2(sky_dictInsert("user_email"), Lib_Db_GetField("email", user), order)) };  if true { return order };  return nil }() }() }()
}