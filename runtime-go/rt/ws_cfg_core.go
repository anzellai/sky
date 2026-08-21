package rt

// webSocketUpgradeCfg packs everything the upgrade dispatcher needs
// from the user's Sky-side cfg record.
type webSocketUpgradeCfg struct {
	onConnect       any
	onMessage       any
	onClose         any
	onError         any
	maxMessageBytes int
	originPatterns  []string
}
