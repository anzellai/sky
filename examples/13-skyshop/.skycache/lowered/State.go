var State_HomePage = map[string]any{"Tag": 0, "SkyName": "HomePage"}

var State_ProductsPage = map[string]any{"Tag": 1, "SkyName": "ProductsPage"}

func State_ProductPage(v0 any) any {
	return map[string]any{"Tag": 2, "SkyName": "ProductPage", "V0": v0}
}

var State_CartPage = map[string]any{"Tag": 3, "SkyName": "CartPage"}

var State_OrdersPage = map[string]any{"Tag": 4, "SkyName": "OrdersPage"}

func State_OrderPage(v0 any) any {
	return map[string]any{"Tag": 5, "SkyName": "OrderPage", "V0": v0}
}

func State_OrderSuccessPage(v0 any) any {
	return map[string]any{"Tag": 6, "SkyName": "OrderSuccessPage", "V0": v0}
}

var State_AuthSignInPage = map[string]any{"Tag": 7, "SkyName": "AuthSignInPage"}

var State_AdminProductsPage = map[string]any{"Tag": 8, "SkyName": "AdminProductsPage"}

var State_AdminNewProductPage = map[string]any{"Tag": 9, "SkyName": "AdminNewProductPage"}

func State_AdminEditProductPage(v0 any) any {
	return map[string]any{"Tag": 10, "SkyName": "AdminEditProductPage", "V0": v0}
}

var State_AdminOrdersPage = map[string]any{"Tag": 11, "SkyName": "AdminOrdersPage"}

func State_AdminOrderPage(v0 any) any {
	return map[string]any{"Tag": 12, "SkyName": "AdminOrderPage", "V0": v0}
}

var State_PrivacyPolicyPage = map[string]any{"Tag": 13, "SkyName": "PrivacyPolicyPage"}

var State_TermsPage = map[string]any{"Tag": 14, "SkyName": "TermsPage"}

var State_NotFoundPage = map[string]any{"Tag": 15, "SkyName": "NotFoundPage"}

func State_Navigate(v0 any) any {
	return map[string]any{"Tag": 0, "SkyName": "Navigate", "V0": v0}
}

func State_SetLang(v0 any) any {
	return map[string]any{"Tag": 1, "SkyName": "SetLang", "V0": v0}
}

var State_DoSignOut = map[string]any{"Tag": 2, "SkyName": "DoSignOut"}

func State_SetSearch(v0 any) any {
	return map[string]any{"Tag": 3, "SkyName": "SetSearch", "V0": v0}
}

func State_SetCategoryFilter(v0 any) any {
	return map[string]any{"Tag": 4, "SkyName": "SetCategoryFilter", "V0": v0}
}

func State_SetSortBy(v0 any) any {
	return map[string]any{"Tag": 5, "SkyName": "SetSortBy", "V0": v0}
}

var State_ToggleSortDir = map[string]any{"Tag": 6, "SkyName": "ToggleSortDir"}

func State_SelectProduct(v0 any) any {
	return map[string]any{"Tag": 7, "SkyName": "SelectProduct", "V0": v0}
}

func State_UpdateQuantity(v0 any) any {
	return map[string]any{"Tag": 8, "SkyName": "UpdateQuantity", "V0": v0}
}

func State_AddToCart(v0 any) any {
	return map[string]any{"Tag": 9, "SkyName": "AddToCart", "V0": v0}
}

func State_UpdateCartItemQty(v0 any, v1 any) any {
	return map[string]any{"Tag": 10, "SkyName": "UpdateCartItemQty", "V0": v0, "V1": v1}
}

func State_RemoveCartItem(v0 any) any {
	return map[string]any{"Tag": 11, "SkyName": "RemoveCartItem", "V0": v0}
}

func State_UpdateRemarks(v0 any) any {
	return map[string]any{"Tag": 12, "SkyName": "UpdateRemarks", "V0": v0}
}

var State_StartCheckout = map[string]any{"Tag": 13, "SkyName": "StartCheckout"}

func State_VerifyPayment(v0 any) any {
	return map[string]any{"Tag": 14, "SkyName": "VerifyPayment", "V0": v0}
}

func State_UpdateEditTitle(v0 any) any {
	return map[string]any{"Tag": 15, "SkyName": "UpdateEditTitle", "V0": v0}
}

func State_UpdateEditSummary(v0 any) any {
	return map[string]any{"Tag": 16, "SkyName": "UpdateEditSummary", "V0": v0}
}

func State_UpdateEditCategory(v0 any) any {
	return map[string]any{"Tag": 17, "SkyName": "UpdateEditCategory", "V0": v0}
}

func State_UpdateEditPrice(v0 any) any {
	return map[string]any{"Tag": 18, "SkyName": "UpdateEditPrice", "V0": v0}
}

func State_UpdateEditDiscount(v0 any) any {
	return map[string]any{"Tag": 19, "SkyName": "UpdateEditDiscount", "V0": v0}
}

func State_UpdateEditCurrency(v0 any) any {
	return map[string]any{"Tag": 20, "SkyName": "UpdateEditCurrency", "V0": v0}
}

func State_UpdateEditStock(v0 any) any {
	return map[string]any{"Tag": 21, "SkyName": "UpdateEditStock", "V0": v0}
}

var State_ToggleEditPublished = map[string]any{"Tag": 22, "SkyName": "ToggleEditPublished"}

var State_SubmitProduct = map[string]any{"Tag": 23, "SkyName": "SubmitProduct"}

func State_DeleteProduct(v0 any) any {
	return map[string]any{"Tag": 24, "SkyName": "DeleteProduct", "V0": v0}
}

func State_UpdateImageData(v0 any) any {
	return map[string]any{"Tag": 25, "SkyName": "UpdateImageData", "V0": v0}
}

var State_UploadImage = map[string]any{"Tag": 26, "SkyName": "UploadImage"}

func State_DeleteImage(v0 any) any {
	return map[string]any{"Tag": 27, "SkyName": "DeleteImage", "V0": v0}
}

func State_SetAdminOrderFilter(v0 any) any {
	return map[string]any{"Tag": 28, "SkyName": "SetAdminOrderFilter", "V0": v0}
}

func State_UpdateOrderState(v0 any, v1 any) any {
	return map[string]any{"Tag": 29, "SkyName": "UpdateOrderState", "V0": v0, "V1": v1}
}

var State_DismissNotification = map[string]any{"Tag": 30, "SkyName": "DismissNotification"}

func State_FirebaseAuth(v0 any) any {
	return map[string]any{"Tag": 31, "SkyName": "FirebaseAuth", "V0": v0}
}

var State_Tick = map[string]any{"Tag": 32, "SkyName": "Tick"}

var State_NoOp = map[string]any{"Tag": 33, "SkyName": "NoOp"}

func State_EmptyModel() any {
	return map[string]any{"page": map[string]any{"Tag": 0, "SkyName": "HomePage"}, "lang": "en", "user": SkyNothing(), "authError": "", "products": []any{}, "product": SkyNothing(), "productImages": []any{}, "search": "", "categoryFilter": "all", "sortBy": "newest", "sortDir": "desc", "cart": SkyNothing(), "cartItems": []any{}, "cartRemarks": "", "checkoutUrl": "", "orders": []any{}, "order": SkyNothing(), "orderItems": []any{}, "editProductId": "", "editTitle": "", "editSummary": "", "editCategory": "Miscellaneous", "editPrice": 0, "editDiscount": 0, "editCurrency": "GBP", "editStock": -1, "editPublished": false, "editImageData": "", "adminOrders": []any{}, "adminOrderFilter": "all", "quantity": 1, "notification": "", "notificationType": "", "signOutPending": false}
}

func State_AllCategories() any {
	return []any{"Accessories", "Bags", "Fashion", "Jewellery And Watches", "Kids", "Luxury Brands", "Miscellaneous", "Wallet And Small Items"}
}

func State_AllCartStates() any {
	return []any{"ongoing", "pending", "ordered", "shipped", "delivered", "cancelled", "refunded"}
}

func State_AllCurrencies() any {
	return []any{"GBP", "HKD", "USD"}
}