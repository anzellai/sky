// hash:3-1
var State_BoardPage = map[string]any{"Tag": 0, "SkyName": "BoardPage"}

var State_DetailPage = map[string]any{"Tag": 1, "SkyName": "DetailPage"}

var State_SubmitPage = map[string]any{"Tag": 2, "SkyName": "SubmitPage"}

var State_SignInPage = map[string]any{"Tag": 3, "SkyName": "SignInPage"}

var State_SignUpPage = map[string]any{"Tag": 4, "SkyName": "SignUpPage"}

var State_AboutPage = map[string]any{"Tag": 5, "SkyName": "AboutPage"}

var State_RoadmapPage = map[string]any{"Tag": 6, "SkyName": "RoadmapPage"}

var State_NotFoundPage = map[string]any{"Tag": 7, "SkyName": "NotFoundPage"}

func State_Navigate(v0 any) any {
	return map[string]any{"Tag": 0, "SkyName": "Navigate", "V0": v0}
}

func State_SetSort(v0 any) any {
	return map[string]any{"Tag": 1, "SkyName": "SetSort", "V0": v0}
}

func State_SetFilter(v0 any) any {
	return map[string]any{"Tag": 2, "SkyName": "SetFilter", "V0": v0}
}

func State_SetSearch(v0 any) any {
	return map[string]any{"Tag": 3, "SkyName": "SetSearch", "V0": v0}
}

func State_ToggleVote(v0 any) any {
	return map[string]any{"Tag": 4, "SkyName": "ToggleVote", "V0": v0}
}

func State_ViewIdea(v0 any) any {
	return map[string]any{"Tag": 5, "SkyName": "ViewIdea", "V0": v0}
}

func State_UpdateComment(v0 any) any {
	return map[string]any{"Tag": 6, "SkyName": "UpdateComment", "V0": v0}
}

var State_SubmitComment = map[string]any{"Tag": 7, "SkyName": "SubmitComment"}

var State_BackToBoard = map[string]any{"Tag": 8, "SkyName": "BackToBoard"}

func State_UpdateTitle(v0 any) any {
	return map[string]any{"Tag": 9, "SkyName": "UpdateTitle", "V0": v0}
}

func State_UpdateDescription(v0 any) any {
	return map[string]any{"Tag": 10, "SkyName": "UpdateDescription", "V0": v0}
}

func State_UpdateCategory(v0 any) any {
	return map[string]any{"Tag": 11, "SkyName": "UpdateCategory", "V0": v0}
}

var State_SubmitIdea = map[string]any{"Tag": 12, "SkyName": "SubmitIdea"}

func State_UpdateEmail(v0 any) any {
	return map[string]any{"Tag": 13, "SkyName": "UpdateEmail", "V0": v0}
}

func State_UpdateUsername(v0 any) any {
	return map[string]any{"Tag": 14, "SkyName": "UpdateUsername", "V0": v0}
}

func State_UpdatePassword(v0 any) any {
	return map[string]any{"Tag": 15, "SkyName": "UpdatePassword", "V0": v0}
}

var State_DoSignIn = map[string]any{"Tag": 16, "SkyName": "DoSignIn"}

var State_DoSignUp = map[string]any{"Tag": 17, "SkyName": "DoSignUp"}

var State_DoSignOut = map[string]any{"Tag": 18, "SkyName": "DoSignOut"}

var State_Tick = map[string]any{"Tag": 19, "SkyName": "Tick"}