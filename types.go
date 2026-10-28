package voltkit

import "encoding/json"

// The wire types, as the controller and the website API send them (spec
// section 6). Every struct accepts fields it does not know (the wire changes
// additively), metadata keeps its shape as json.RawMessage, and money stays a
// string: a JSON number is a float, and a float rounds a large balance
// without telling you.

// Role is what a participant may do in a room. Your backend decides it; it is
// signed into the token and the client cannot change it.
type Role string

// The four roles: a host owns the room, a cohost speaks beside them, a
// moderator keeps order, a viewer watches and chats.
const (
	RoleHost      Role = "host"
	RoleCohost    Role = "cohost"
	RoleModerator Role = "moderator"
	RoleViewer    Role = "viewer"
)

// PlaybackPolicy says who may watch a broadcast stream.
type PlaybackPolicy string

const (
	// PlaybackPublic: anyone with the playback URL. The default.
	PlaybackPublic PlaybackPolicy = "public"
	// PlaybackToken: each viewer needs URLs minted with CreateStreamToken.
	PlaybackToken PlaybackPolicy = "token"
)

// StreamAction is what a stream token is for.
type StreamAction string

const (
	// ActionPlay: a viewer of a token-protected stream. The default.
	ActionPlay StreamAction = "play"
	// ActionPublish: an encoder whose publish URL has expired.
	ActionPublish StreamAction = "publish"
)

// --- rooms --------------------------------------------------------------------

// Room is a live room as the controller describes it.
type Room struct {
	RoomID       string `json:"roomId"`
	TenantID     string `json:"tenantId"`
	OriginNode   string `json:"originNode"`
	State        string `json:"state"`
	HostIdentity string `json:"hostIdentity"`
	// CreatedAt is unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// Nodes are the media nodes the room spans.
	Nodes []string `json:"nodes"`
	// Participants is how many are connected right now.
	Participants int  `json:"participants"`
	Demo         bool `json:"demo,omitempty"`
	// Metadata is the opaque, room-scoped state given when the room was
	// created; every participant receives it on join.
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// RoomWithHostToken is what CreateRoom and EnsureRoom return: the room and a
// fresh host token.
type RoomWithHostToken struct {
	Room
	// HostToken is a credential for this room: hand it to the host's client,
	// never log it.
	HostToken string `json:"hostToken"`
	WSURL     string `json:"wsUrl"`
	// ExpiresAt is unix seconds.
	ExpiresAt int64 `json:"expiresAt"`
	// Existed is true when the room already existed and was returned rather
	// than created (EnsureRoom answered 200, not 201). Always false from
	// CreateRoom.
	Existed bool `json:"existed"`
}

// RoomToken is a room token for one identity, from CreateToken.
type RoomToken struct {
	Token    string `json:"token"`
	WSURL    string `json:"wsUrl"`
	Role     Role   `json:"role"`
	Identity string `json:"identity"`
	RoomID   string `json:"roomId"`
	// ExpiresAt is unix seconds. A token lasts ten minutes; a connected
	// client is refreshed by the controller.
	ExpiresAt int64 `json:"expiresAt"`
}

// Participant is one connected identity in a room.
type Participant struct {
	Identity     string          `json:"identity"`
	Role         Role            `json:"role"`
	NodeID       string          `json:"nodeId"`
	HandRaised   bool            `json:"handRaised"`
	HandMetadata json.RawMessage `json:"handMetadata,omitempty"`
	Kicked       bool            `json:"kicked,omitempty"`
	// Muted and VideoBlocked are moderator state: a moderator switched the
	// mic or camera off and the participant may not switch it back.
	Muted        bool `json:"muted"`
	VideoBlocked bool `json:"videoBlocked"`
	// JoinedAt is unix milliseconds.
	JoinedAt int64 `json:"joinedAt"`
	// Metadata is their display metadata, from the token or their own
	// setMetadata.
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// Producer is one published track in a room, as GetRoom lists them.
type Producer struct {
	ProducerID string `json:"producerId"`
	Identity   string `json:"identity"`
	// Kind is audio or video.
	Kind   string `json:"kind"`
	NodeID string `json:"nodeId"`
	Paused bool   `json:"paused"`
	// Source is mic or music for audio, camera or screen for video; empty
	// means mic or camera.
	Source string `json:"source,omitempty"`
}

// RoomView is GetRoom's answer: the authoritative live view.
type RoomView struct {
	Room         Room          `json:"room"`
	Participants []Participant `json:"participants"`
	Producers    []Producer    `json:"producers"`
}

// RoomList is ListRooms's answer. The wire wraps the list in an object (so a
// later release can add fields beside it), and so does this SDK, like every
// VoltKit server SDK.
type RoomList struct {
	Rooms []Room `json:"rooms"`
}

// --- chat ---------------------------------------------------------------------

// ChatToken is a chat-only token, from CreateChatToken.
type ChatToken struct {
	Token string `json:"token"`
	// WSURL is where a chat-only client connects. Ends in /ws/chat.
	WSURL    string `json:"wsUrl"`
	Role     Role   `json:"role"`
	Identity string `json:"identity"`
	Channel  string `json:"channel"`
	// RoomID is the room of the same name, when the wire names it.
	RoomID string `json:"roomId,omitempty"`
	// ExpiresAt is unix seconds.
	ExpiresAt int64 `json:"expiresAt"`
}

// ChatInfo is GetChatInfo's answer.
type ChatInfo struct {
	Channel string `json:"channel"`
	// Members is how many identities are connected to the channel, in the
	// room or only in its chat.
	Members int `json:"members"`
}

// ChatMessage is one stored message.
type ChatMessage struct {
	// ID is monotonic within a channel: the oldest id you hold is a paging
	// cursor for ListMessages.
	ID      string `json:"id"`
	Channel string `json:"channel"`
	From    string `json:"from"`
	// To is set on a direct message.
	To   string `json:"to,omitempty"`
	Type string `json:"type"`
	// Text is delivered exactly as sent. Render it as text, never as HTML.
	Text        string          `json:"text,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	ClientMsgID string          `json:"clientMsgId,omitempty"`
	// TS is unix milliseconds.
	TS int64 `json:"ts"`
}

// --- broadcast ----------------------------------------------------------------

// PlaybackURLs is where a stream is watched.
type PlaybackURLs struct {
	LLHLS  string `json:"llhls"`
	WebRTC string `json:"webrtc"`
	Token  string `json:"token,omitempty"`
	// ExpiresAt is unix seconds, when a token is present.
	ExpiresAt int64 `json:"expiresAt,omitempty"`
}

// BroadcastStream is a stream as the controller describes it.
type BroadcastStream struct {
	StreamID string `json:"streamId"`
	TenantID string `json:"tenantId"`
	// StreamKey is the name the media server knows the stream by. Random, so
	// a playback URL cannot be derived from a stream id.
	StreamKey string `json:"streamKey"`
	// State is idle or live.
	State    string         `json:"state"`
	Playback PlaybackPolicy `json:"playback"`
	// CreatedAt, StartedAt and EndedAt are unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	StartedAt int64 `json:"startedAt,omitempty"`
	EndedAt   int64 `json:"endedAt,omitempty"`
	// Viewers counts the playback sessions the media server serves itself.
	// An audience behind a CDN is not in this number.
	Viewers      int             `json:"viewers"`
	PeakViewers  int             `json:"peakViewers"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	PlaybackURLs PlaybackURLs    `json:"playbackUrls"`
}

// PublishInfo is how an encoder publishes: WHIPURL already carries the token.
type PublishInfo struct {
	WHIPURL string `json:"whipUrl"`
	Token   string `json:"token"`
	// ExpiresAt is unix seconds.
	ExpiresAt int64 `json:"expiresAt"`
}

// BroadcastStreamWithPublish is CreateStream's answer: the only reply that
// ever carries a publish token. Treat Publish like a password.
type BroadcastStreamWithPublish struct {
	BroadcastStream
	Publish PublishInfo `json:"publish"`
}

// StreamToken is CreateStreamToken's answer: WHIPURL for publish, LLHLS and
// WebRTC for play.
type StreamToken struct {
	StreamID string       `json:"streamId"`
	Action   StreamAction `json:"action"`
	Token    string       `json:"token"`
	// ExpiresAt is unix seconds.
	ExpiresAt int64  `json:"expiresAt"`
	WHIPURL   string `json:"whipUrl,omitempty"`
	LLHLS     string `json:"llhls,omitempty"`
	WebRTC    string `json:"webrtc,omitempty"`
}

// --- usage --------------------------------------------------------------------

// Usage is one period's counters.
type Usage struct {
	ParticipantSeconds     int64 `json:"participantSeconds"`
	PublisherSeconds       int64 `json:"publisherSeconds"`
	RoomSeconds            int64 `json:"roomSeconds"`
	RoomsCreated           int64 `json:"roomsCreated"`
	PeakParticipants       int64 `json:"peakParticipants"`
	ChatMessages           int64 `json:"chatMessages"`
	ChatConnectionSeconds  int64 `json:"chatConnectionSeconds"`
	BroadcastSeconds       int64 `json:"broadcastSeconds"`
	BroadcastViewerSeconds int64 `json:"broadcastViewerSeconds"`
	StreamsStarted         int64 `json:"streamsStarted"`
	// BillableSeconds is everything that counts against an allowance: room
	// plus broadcast seconds. FreeSeconds is the part a free allowance
	// covered. Both are absent on older controllers.
	BillableSeconds int64 `json:"billableSeconds,omitempty"`
	FreeSeconds     int64 `json:"freeSeconds,omitempty"`
}

// Allowance is where an account stands against every limit, in seconds;
// -1 means no limit.
type Allowance struct {
	Plan                 string `json:"plan"`
	FreeDailyMinutes     int64  `json:"freeDailyMinutes"`
	FreeMonthlyMinutes   int64  `json:"freeMonthlyMinutes"`
	DailyCapMinutes      int64  `json:"dailyCapMinutes"`
	MonthlyCapMinutes    int64  `json:"monthlyCapMinutes"`
	UsedTodaySeconds     int64  `json:"usedTodaySeconds"`
	UsedMonthSeconds     int64  `json:"usedMonthSeconds"`
	FreeUsedTodaySeconds int64  `json:"freeUsedTodaySeconds"`
	FreeUsedMonthSeconds int64  `json:"freeUsedMonthSeconds"`
	FreeRemainingSeconds int64  `json:"freeRemainingSeconds"`
	PaidRemainingSeconds int64  `json:"paidRemainingSeconds"`
	RemainingSeconds     int64  `json:"remainingSeconds"`
	// Blocked is why the account cannot be used right now, if it cannot:
	// quota-exhausted, daily-limit-reached, monthly-limit-reached or
	// suspended.
	Blocked string `json:"blocked,omitempty"`
}

// UsageSummary is GetUsage's answer.
type UsageSummary struct {
	TenantID string `json:"tenantId"`
	// Plan is metered or unlimited; Status active or suspended.
	Plan              string `json:"plan"`
	Status            string `json:"status"`
	MinutesLimit      int64  `json:"minutesLimit"`
	UsedMinutes       int64  `json:"usedMinutes"`
	SharedUsedMinutes int64  `json:"sharedUsedMinutes"`
	// MinutesRemaining is -1 on the unlimited plan.
	MinutesRemaining int64 `json:"minutesRemaining"`
	Today            Usage `json:"today"`
	Month            Usage `json:"month"`
	Lifetime         Usage `json:"lifetime"`
	// Allowance is free allowances, paid balance and caps, right now.
	Allowance *Allowance `json:"allowance,omitempty"`
}

// DailyUsage is one UTC day of GetUsageSeries.
type DailyUsage struct {
	// Day is YYYY-MM-DD, UTC.
	Day string `json:"day"`
	Usage
}

// --- billing ------------------------------------------------------------------
//
// Money is credit units (cu): 1 credit = 6,000,000 cu (CuPerCredit). Every cu
// amount is a decimal string, never a number: use ParseCu for arithmetic.

// LegacyFunds are the minute pockets spent before the wallet, in
// participant-seconds.
type LegacyFunds struct {
	// Unlimited: a plan whose own minutes never run out; OwnSeconds and
	// TotalSeconds are then -1.
	Unlimited bool `json:"unlimited"`
	// FreeSeconds is the free allowance left.
	FreeSeconds int64 `json:"freeSeconds"`
	// OwnSeconds is the project's own minutes; -1 when unlimited.
	OwnSeconds int64 `json:"ownSeconds"`
	// SharedSeconds is the account balance the project draws on, if any.
	SharedSeconds int64 `json:"sharedSeconds"`
	// TotalSeconds is free + own + shared; -1 when unlimited.
	TotalSeconds int64 `json:"totalSeconds"`
}

// WalletScopeAmount is the credits in one wallet scope, by origin. Promo is
// spent before paid.
type WalletScopeAmount struct {
	PaidCu  string `json:"paidCu"`
	PromoCu string `json:"promoCu"`
}

// WalletFunds is the wallet credits this project may spend. Only its own
// scopes are listed: another project's allocation is never shown.
type WalletFunds struct {
	// SpendOrder is the scopes in the order they are spent:
	// project:<tenantId>, product:rtc, unallocated.
	SpendOrder []string                     `json:"spendOrder"`
	Scopes     map[string]WalletScopeAmount `json:"scopes"`
	PaidCu     string                       `json:"paidCu"`
	PromoCu    string                       `json:"promoCu"`
	// AvailableCu is PaidCu + PromoCu: everything this project may spend from
	// the wallet now.
	AvailableCu string `json:"availableCu"`
	// AvailableCredits is AvailableCu in credits, at most four decimals,
	// rounded down ("31", "10.62"). For display only.
	AvailableCredits string `json:"availableCredits"`
}

// BurnView is what is being spent right now.
type BurnView struct {
	// LiveParticipants are joined participants across all the project's rooms.
	LiveParticipants int `json:"liveParticipants"`
	// ProjectCuPerSecond is cu per second for this project's rooms and
	// broadcasts; WalletCuPerSecond for every project drawing the same wallet.
	ProjectCuPerSecond string `json:"projectCuPerSecond"`
	WalletCuPerSecond  string `json:"walletCuPerSecond"`
}

// WalletLowState is the low-funds warning rooms get before their funds run out.
type WalletLowState struct {
	// Active: the project's rooms are being warned now.
	Active bool `json:"active"`
	// GraceSeconds: the warning starts when what is left covers less than
	// this many seconds at the current burn.
	GraceSeconds int64 `json:"graceSeconds"`
}

// ProjectBalance is GetBalance's answer: the project's spendable funds right
// now. Read only; recharges happen on the website.
type ProjectBalance struct {
	TenantID string `json:"tenantId"`
	// BillingMode is legacy (flat minutes) or wallet (credits from a wallet).
	BillingMode string `json:"billingMode"`
	// CanJoin is whether a newcomer would be admitted now.
	CanJoin bool `json:"canJoin"`
	// Blocked is, when CanJoin is false, the code a join or a token mint is
	// refused with; nil otherwise.
	Blocked *string     `json:"blocked"`
	Legacy  LegacyFunds `json:"legacy"`
	// Wallet is nil unless the project is billed from its wallet.
	Wallet *WalletFunds `json:"wallet"`
	Burn   BurnView     `json:"burn"`
	// RemainingSeconds estimates how long the funds last at the current burn;
	// -1 when nothing is being spent or nothing limits it.
	RemainingSeconds int64          `json:"remainingSeconds"`
	WalletLow        WalletLowState `json:"walletLow"`
	// PricingVersion is the price list new wallet sessions lock; nil for a
	// legacy project, and while no price list is active.
	PricingVersion *int `json:"pricingVersion"`
	// UpdatedAt is unix milliseconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// --- rates --------------------------------------------------------------------

// RtcUnit names the units of a price list, and cu per credit ("6000000").
type RtcUnit struct {
	Price       string `json:"price"`
	Rate        string `json:"rate"`
	CuPerCredit string `json:"cuPerCredit"`
}

// RtcTierRate is a billing tier. A participant is billed at one tier at a
// time: the highest media they send or receive.
type RtcTierRate struct {
	// ID is stable (audio_std, video_30, video_720_60, ...): usage and the
	// ledger use it. Prices are what change, from version to version.
	ID string `json:"id"`
	// Rank: higher wins when a participant does several things at once.
	Rank  int    `json:"rank"`
	Label string `json:"label"`
	Media string `json:"media"`
	// Credits per 1,000 participant-minutes, e.g. "99.00".
	Credits string `json:"credits"`
	// CuPerParticipantSecond is the same price as an exact integer, e.g. "9900".
	CuPerParticipantSecond string `json:"cuPerParticipantSecond"`
	// Base is the family price a 60 fps surcharge is added to; Surcharge the
	// surcharge included in Credits, nil for a tier without one.
	Base      string  `json:"base"`
	Surcharge *string `json:"surcharge"`
	// Available: some enabled media mode bills at this tier.
	Available bool `json:"available"`
}

// RtcSurcharge is the 60 fps premium at one resolution, over the video base.
type RtcSurcharge struct {
	Tier   string `json:"tier"`
	Height int    `json:"height"`
	FPS    int    `json:"fps"`
	// Credits per 1,000 participant-minutes.
	Credits string `json:"credits"`
}

// RtcAddonRate is what an add-on costs: billed on top of a participant's
// tier while the add-on is in use (0.1.2). The Music add-on (Addons.Music):
// in-app music is billed on standard and HD voice while music plays, and
// included with video. "music" is an add-on id, not a tier: a usage line with
// tier "music" is the add-on's participant-minutes next to the participant's
// own tier line.
type RtcAddonRate struct {
	// ID is the add-on id ("music") and Label its name ("Music add-on"), when
	// the API sends them.
	ID    string `json:"id,omitempty"`
	Label string `json:"label,omitempty"`
	// Price is credits per 1,000 participant-minutes while the add-on is in
	// use, e.g. "10.00" ("0.00" when this price list does not charge for it);
	// CuPerParticipantSecond the same price as an exact integer, e.g. "1000",
	// when sent.
	Price                  string `json:"price"`
	CuPerParticipantSecond string `json:"cuPerParticipantSecond,omitempty"`
	// Unit is the unit in words, e.g. "credits per 1,000 participant-minutes
	// while music plays".
	Unit string `json:"unit"`
	// AppliesTo names the tiers it is billed on top of (music: audio_std,
	// audio_hd); IncludedIn the tiers that include it at no extra charge
	// (music: every video tier).
	AppliesTo  []string `json:"appliesTo"`
	IncludedIn []string `json:"includedIn"`
}

// RtcAddons is the add-ons of a price list, by id.
type RtcAddons struct {
	// Music is the Music add-on: ₹10 per 1,000 participant-minutes while
	// music plays, on standard and HD voice; included with video. Enabled per
	// project under Settings → Media and quality. Nil when the list has none.
	Music *RtcAddonRate `json:"music,omitempty"`
}

// ReceiveKbps is what one viewer receives at the top layer.
type ReceiveKbps struct {
	Video int `json:"video"`
	Audio int `json:"audio"`
	Total int `json:"total"`
}

// RtcMode is an enabled media mode: what it sends, and the tier it bills as.
// Kind is audio (Channels set) or video (Width, Height, FPS, Layers set).
type RtcMode struct {
	ProfileID string `json:"profileId"`
	Label     string `json:"label"`
	Tier      string `json:"tier"`
	TierLabel string `json:"tierLabel"`
	Enabled   bool   `json:"enabled"`
	// Codecs are MIME types, e.g. "video/VP8".
	Codecs      []string    `json:"codecs"`
	ReceiveKbps ReceiveKbps `json:"receiveKbps"`
	Kind        string      `json:"kind"`
	Channels    int         `json:"channels,omitempty"`
	Width       int         `json:"width,omitempty"`
	Height      int         `json:"height,omitempty"`
	FPS         int         `json:"fps,omitempty"`
	Layers      int         `json:"layers,omitempty"`
}

// RtcConversion is credits per rupee for new recharges, exactly and for display.
type RtcConversion struct {
	// CreditsPerRupee has four decimals; the exact rate is
	// Numerator / Denominator credits per rupee.
	CreditsPerRupee string `json:"creditsPerRupee"`
	Numerator       string `json:"numerator"`
	Denominator     string `json:"denominator"`
	InrPerCredit    string `json:"inrPerCredit"`
	// Version is nil while the default (1 rupee = 1 credit) applies;
	// EffectiveAt is ISO 8601, nil for the default.
	Version     *int    `json:"version"`
	EffectiveAt *string `json:"effectiveAt"`
	// Source is conversion-version or default.
	Source string `json:"source"`
}

// RtcRates is the public price list, from GetRates. Prices only, never costs.
type RtcRates struct {
	// Version is the price list version. A wallet session keeps the version
	// it started under.
	Version int `json:"version"`
	// Kind is standard here: a contract list is one customer's negotiated
	// rates, never public.
	Kind string `json:"kind"`
	// EffectiveAt is when this version took effect, ISO 8601.
	EffectiveAt *string        `json:"effectiveAt"`
	Unit        RtcUnit        `json:"unit"`
	Tiers       []RtcTierRate  `json:"tiers"`
	Surcharges  []RtcSurcharge `json:"surcharges"`
	// Addons are billed on top of a tier while in use (0.1.2): Music, the
	// Music add-on. Nil for a price list older than the add-on.
	Addons *RtcAddons `json:"addons,omitempty"`
	// IdleTier is what a participant who is only connected is billed as:
	// audio_std or none.
	IdleTier   string        `json:"idleTier"`
	Modes      []RtcMode     `json:"modes"`
	Conversion RtcConversion `json:"conversion"`
	// Notes are the billing rules in plain words, for a pricing page.
	Notes []string `json:"notes"`
}

// --- webhooks -----------------------------------------------------------------

// StreamEvent is one webhook delivery. Data's shape varies by Type; see
// https://voltkit.in/docs/webhooks.
type StreamEvent struct {
	// ID is stable across redeliveries: deduplicate on it.
	ID       string `json:"id"`
	Type     string `json:"type"`
	TenantID string `json:"tenantId"`
	RoomID   string `json:"roomId,omitempty"`
	// CreatedAt is unix milliseconds.
	CreatedAt int64           `json:"createdAt"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// --- requests -----------------------------------------------------------------
//
// Optional fields are left out of the body when they are zero, never sent as
// null (spec section 3.3): the server refuses unknown fields and nulls where it
// expects a value. Ids that go into the path are tagged json:"-".

// CreateRoomParams is CreateRoom's argument.
type CreateRoomParams struct {
	// RoomID is your id, so webhooks read back usefully. Left out, the
	// server makes one up.
	RoomID       string `json:"roomId,omitempty"`
	HostIdentity string `json:"hostIdentity"`
	// Metadata is any JSON value: opaque, room-scoped state every participant
	// receives when they join.
	Metadata any `json:"metadata,omitempty"`
}

// EnsureRoomParams is EnsureRoom's argument.
type EnsureRoomParams struct {
	RoomID       string `json:"-"`
	HostIdentity string `json:"hostIdentity"`
	// Metadata is applied only when the room is created.
	Metadata any `json:"metadata,omitempty"`
}

// CreateTokenParams is CreateToken's argument. Role is required: your backend
// decides it, and the server refuses an unknown one with bad-role.
type CreateTokenParams struct {
	RoomID   string `json:"-"`
	Identity string `json:"identity"`
	Role     Role   `json:"role"`
}

// CreateChatTokenParams is CreateChatToken's argument. Role defaults to viewer.
type CreateChatTokenParams struct {
	Channel  string `json:"-"`
	Identity string `json:"identity"`
	Role     Role   `json:"role,omitempty"`
}

// ListMessagesOptions pages through history. Both are optional: Limit
// defaults to 50 on the server and is capped at 100; Before is the oldest id
// you hold.
type ListMessagesOptions struct {
	Limit  int
	Before string
}

// SendMessageParams is SendMessage's argument. A message needs Text or
// Metadata.
type SendMessageParams struct {
	Channel string `json:"-"`
	Text    string `json:"text,omitempty"`
	// Type defaults to "text". Anything else is your own vocabulary.
	Type string `json:"type,omitempty"`
	// From is who it is from. Defaults to "system".
	From string `json:"from,omitempty"`
	// To makes it a direct message to one identity.
	To       string `json:"to,omitempty"`
	Metadata any    `json:"metadata,omitempty"`
	// Ephemeral delivers without storing: for reactions and anything not
	// worth replaying.
	Ephemeral bool `json:"ephemeral,omitempty"`
}

// CreateStreamParams is CreateStream's argument.
type CreateStreamParams struct {
	StreamID string         `json:"streamId"`
	Playback PlaybackPolicy `json:"playback,omitempty"`
	Metadata any            `json:"metadata,omitempty"`
}

// UpdateStreamParams is UpdateStream's patch: who may watch, or the metadata.
type UpdateStreamParams struct {
	Playback PlaybackPolicy `json:"playback,omitempty"`
	Metadata any            `json:"metadata,omitempty"`
}

// CreateStreamTokenParams is CreateStreamToken's argument. Action defaults to
// play; TTLSeconds to the service's default and is capped at 24 hours.
type CreateStreamTokenParams struct {
	StreamID   string       `json:"-"`
	Action     StreamAction `json:"action,omitempty"`
	Identity   string       `json:"identity,omitempty"`
	TTLSeconds int          `json:"ttlSeconds,omitempty"`
}
