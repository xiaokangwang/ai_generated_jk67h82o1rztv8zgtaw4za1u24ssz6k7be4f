package main

type Response struct {
	Message string `json:"message"`
}

type GitHubAccessTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
}

type GitHubUser struct {
	Login string `json:"login"`
}

type UserResponse struct {
	Username        string `json:"username"`
	IsCoreStargazer bool   `json:"is_core_stargazer"`
}

type UserState struct {
	UsedInverseModeServerSlot      uint64   `json:"used_inverse_mode_server_slot"`
	AssociatedForwarderServerSlots []uint64 `json:"associated_forwarder_server_slots"`
}

type ServerState struct {
	UsedInverseModeServerSlot uint64 `json:"used_inverse_mode_server_slot"`
}

type GenerateTokenRequest struct {
	SlotID uint64 `json:"slot_id"`
}

type GenerateTokenResponse struct {
	Private string `json:"private"`
	Public  string `json:"public"`
}
