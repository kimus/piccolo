// Package setup implements the interactive editing counterpart of
// piccolo init: arrow-navigable editors that merge-patch the user-owned
// config.json (never regenerate it) behind a diff preview + confirmation.
package setup

import "strings"

// FieldType is the input kind for one channel settings field.
type FieldType int

const (
	// Text is a plain single-line input.
	Text FieldType = iota
	// Secret is a password-echoed input (stored plaintext in config.json,
	// like model API keys — PicoClaw has no env expansion, verified v0.3.1).
	Secret
	// CommaList is a comma-separated string list (allow_from user IDs).
	CommaList
	// BoolText is a yes/no answer rendered as text input with defaults.
	BoolText
)

// FieldSpec describes one prompted value for a channel type.
type FieldSpec struct {
	Key         string    // settings key inside the channel entry
	Title       string    // wizard prompt title
	Type        FieldType //
	Placeholder string    // example value shown in the input
	Optional    bool      // skip when empty
	Help        string    // one-line hint under the title
	Guide       string    // detailed how-to shown as a page BEFORE this field
}

// ChannelType is a provider definition: guided steps + settings schema +
// default config.json shape (verified against picoclaw v0.3.1
// pkg/config + config/config.example.json).
type ChannelType struct {
	Name           string   // channel_list key AND channel type (piccolo uses 1:1)
	Label          string   // human label for menus
	Steps          []string // provider-side setup checklist printed before the form
	Fields         []FieldSpec
	MentionOnly    bool   // supports group_trigger.mention_only
	AllowFromGuide string // detailed how-to for the allowed-user-IDs field
}

const discordAllowFromGuide = `To get the User ID, enable Developer Mode, then copy your ID from your profile:

Desktop or browser
  1. Open User Settings ⚙️ → Advanced → Developer Mode and turn it on.
  2. Find a message you sent and right-click your avatar or username.
  3. Select Copy User ID.

Mobile
  1. Tap your avatar at the bottom right, then ⚙️ → Advanced → Developer Mode.
  2. Open a message you sent and tap your avatar.
  3. Tap ⋯ → Copy User ID.`

// Types lists the channel providers setup can write. Discord is the
// reference flow; telegram/slack share the shape (allow_from + token-ish
// settings) verified against config.example.json of the pinned version.
var Types = []ChannelType{
	{
		Name:  "discord",
		Label: "Discord",
		Steps: []string{
			"1. Open https://discord.com/developers/applications → New Application.",
			"2. Bot tab → Reset Token → copy it (you will paste it in piccolo next).",
			"3. Bot tab → enable PRIVILEGED GATEWAY INTENT: \"Message Content Intent\".",
			"4. OAuth2 → URL Generator: scope \"bot\"; permissions \"Send Messages\" +\n   \"Read Message History\"; open the generated URL to invite the bot to your server.",
			"5. Get your Discord user ID (piccolo shows the exact steps on the next pages).",
			"6. Paste the bot token and your user ID into piccolo's prompts below.",
			"7. Heads-up: with a wrong/disabled token the gateway REFUSES to start\n   (\"failed to start any enabled channels\"). If that happens, disable the channel\n   in config.json or fix the token, then piccolo restart.",
		},
		Fields: []FieldSpec{
			{Key: "token", Title: "Discord bot token", Type: Secret, Placeholder: "from step 2", Help: "stored plaintext in config.json (git-ignored)"},
			{Key: "proxy", Title: "Proxy URL (optional)", Type: Text, Optional: true, Placeholder: "leave empty unless you need one"},
		},
		MentionOnly:    true,
		AllowFromGuide: discordAllowFromGuide,
	},
	{
		Name:  "telegram",
		Label: "Telegram",
		Steps: []string{
			"1. In Telegram, open chat with @BotFather → /newbot → follow prompts.",
			"2. Copy the HTTP API token BotFather returns (looks like 123456:ABC-DEF...).",
			"3. Your numeric user ID: send a message to @userinfobot — it replies with your ID.",
			"4. Paste the token and your user ID into piccolo's prompts below.",
		},
		AllowFromGuide: `To get your numeric Telegram user ID:

  1. In Telegram, search for @userinfobot and press START.
  2. It replies instantly with your numeric ID (e.g. 123456789).
  3. Copy that number into the next field.

Alternative: any channel-management bot (like @getmyid_bot) replies the
same way. IDs of groups you add the bot to are also listed there.`,
		Fields: []FieldSpec{
			{Key: "token", Title: "Telegram bot token", Type: Secret, Placeholder: "from step 2", Help: "stored plaintext in config.json (git-ignored)"},
			{Key: "base_url", Title: "Bot API base URL (optional)", Type: Text, Optional: true, Placeholder: "empty = api.telegram.org"},
			{Key: "proxy", Title: "Proxy URL (optional)", Type: Text, Optional: true},
		},
	},
	{
		Name:  "slack",
		Label: "Slack",
		Steps: []string{
			"1. Create a Slack app: https://api.slack.com/apps → From scratch.",
			"2. Bot User: add a token scope \"chat:write\"; enable Socket Mode support.",
			"3. Install app to workspace → copy the Bot User OAuth Token (xoxb-...).",
			"4. App-Level: Basic Information → App-Level Tokens → generate one with\n   scope \"connections:write\" → copy the token (xapp-...).",
			"5. Your Slack member ID: profile → ⋯ → Copy member ID (starts with U).",
			"6. Paste both tokens and your member ID into piccolo's prompts below.",
		},
		AllowFromGuide: `To get your Slack member ID (starts with U):

  1. In Slack, click your name (top right of any message) → "View profile".
  2. In the profile panel, click ⋯ (More actions) next to your display name.
  3. Select "Copy member ID".

Alternative: open any DM with yourself, click the ⋯ in the top-right →
"View person's profile" → same menu.`,
		Fields: []FieldSpec{
			{Key: "bot_token", Title: "Slack bot token (xoxb-...)", Type: Secret, Placeholder: "from step 3"},
			{Key: "app_token", Title: "Slack app token (xapp-...)", Type: Secret, Placeholder: "from step 4"},
		},
	},
}

// Type returns the channel type by name (case-insensitive).
func Type(name string) (ChannelType, bool) {
	for _, t := range Types {
		if strings.EqualFold(t.Name, name) {
			return t, true
		}
	}
	return ChannelType{}, false
}
