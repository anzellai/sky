package rt

// webSocketUpgradeCfg packs everything the upgrade dispatcher needs
// from the user's Sky-side cfg record.
type webSocketUpgradeCfg struct {
	onConnect any
	onMessage any
	// onFrame + frameMode — Sky.Http.Server.WebSocket.withOnFrame. When
	// frameMode is set the read loop hands onFrame a WebSocketMessage
	// (Text / Binary) and never calls onMessage.
	onFrame         any
	frameMode       bool
	onClose         any
	onError         any
	maxMessageBytes int
	originPatterns  []string
}
