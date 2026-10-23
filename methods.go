package voltkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The 25 calls of spec section 6. Each names its request, whether it is safe
// to repeat and whether a 404 on a retry means done; do does the rest.

// --- rooms --------------------------------------------------------------------

// CreateRoom creates a room and fails with room-exists (409) if the id is
// taken. For "go live", prefer EnsureRoom: a host reconnecting after a dropped
// call hits the existing-room path, and this turns that into two round trips.
// Sent once, never retried: a lost reply would turn a success into a spurious
// room-exists (see the README, "Calls that are never repeated").
func (c *StreamClient) CreateRoom(ctx context.Context, params CreateRoomParams) (*RoomWithHostToken, error) {
	var room RoomWithHostToken
	if _, err := c.do(ctx, call{method: "POST", path: "/v1/rooms", body: params, hasBody: true}, &room); err != nil {
		return nil, err
	}
	room.Existed = false
	return &room, nil
}

// EnsureRoom creates the room, or returns it with a fresh host token if it
// already exists: one round trip whether or not the host has been here
// before. Existed says which happened. Metadata is applied only on creation.
func (c *StreamClient) EnsureRoom(ctx context.Context, params EnsureRoomParams) (*RoomWithHostToken, error) {
	id, err := segment("roomId", params.RoomID)
	if err != nil {
		return nil, err
	}
	var room RoomWithHostToken
	status, err := c.do(ctx, call{method: "PUT", path: "/v1/rooms/" + id, body: params, hasBody: true, safeToRepeat: true}, &room)
	if err != nil {
		return nil, err
	}
	room.Existed = status == 200
	return &room, nil
}

// CreateToken authorises one of your users for a room. Your backend decides
// the role: it is signed into the token and the client cannot change it.
// Minting twice is harmless, so this is retried.
func (c *StreamClient) CreateToken(ctx context.Context, params CreateTokenParams) (*RoomToken, error) {
	id, err := segment("roomId", params.RoomID)
	if err != nil {
		return nil, err
	}
	var token RoomToken
	if _, err := c.do(ctx, call{method: "POST", path: "/v1/rooms/" + id + "/tokens", body: params, hasBody: true, safeToRepeat: true}, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

// GetRoom is the authoritative live view: the room, who is in it and what
// they publish. Poll it for a live count rather than adding up webhook joins
// and leaves.
func (c *StreamClient) GetRoom(ctx context.Context, roomID string) (*RoomView, error) {
	id, err := segment("roomId", roomID)
	if err != nil {
		return nil, err
	}
	var view RoomView
	if _, err := c.do(ctx, call{method: "GET", path: "/v1/rooms/" + id, safeToRepeat: true}, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

// ListRooms lists every live room of the project.
func (c *StreamClient) ListRooms(ctx context.Context) (*RoomList, error) {
	var list RoomList
	if _, err := c.do(ctx, call{method: "GET", path: "/v1/rooms", safeToRepeat: true}, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

// ListParticipants is a point-in-time read of who is in a room, for an admin
// screen or a reconciliation job. It is not a presence feed: for that, take
// the webhooks. An empty room is an empty, non-nil slice.
func (c *StreamClient) ListParticipants(ctx context.Context, roomID string) ([]Participant, error) {
	id, err := segment("roomId", roomID)
	if err != nil {
		return nil, err
	}
	var wrapper struct {
		Participants []Participant `json:"participants"`
	}
	if _, err := c.do(ctx, call{method: "GET", path: "/v1/rooms/" + id + "/participants", safeToRepeat: true}, &wrapper); err != nil {
		return nil, err
	}
	return orEmpty(wrapper.Participants), nil
}

// DeleteRoom ends a room: everyone is disconnected and the room is gone. A
// retry that finds the room gone after a lost reply means the lost attempt
// ended it, and the call succeeds.
func (c *StreamClient) DeleteRoom(ctx context.Context, roomID string) error {
	id, err := segment("roomId", roomID)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, call{method: "DELETE", path: "/v1/rooms/" + id, safeToRepeat: true, goneIsDone: true}, nil)
	return err
}

// --- chat ---------------------------------------------------------------------
//
// A channel shares its name with the room of the same name: the people in
// room "show-42" and the people only in chat "show-42" are one conversation.
// A channel with no room behind it is chat on its own.

// CreateChatToken admits one of your users to a channel's chat, with no media
// rights: for someone who is not in the room, such as an audience watching a
// broadcast. People in the room already have chat on the socket they hold.
func (c *StreamClient) CreateChatToken(ctx context.Context, params CreateChatTokenParams) (*ChatToken, error) {
	channel, err := segment("channel", params.Channel)
	if err != nil {
		return nil, err
	}
	var token ChatToken
	if _, err := c.do(ctx, call{method: "POST", path: "/v1/chat/" + channel + "/tokens", body: params, hasBody: true, safeToRepeat: true}, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

// GetChatInfo says how many identities are connected to a channel, in the
// room or only in its chat.
func (c *StreamClient) GetChatInfo(ctx context.Context, channel string) (*ChatInfo, error) {
	ch, err := segment("channel", channel)
	if err != nil {
		return nil, err
	}
	var info ChatInfo
	if _, err := c.do(ctx, call{method: "GET", path: "/v1/chat/" + ch, safeToRepeat: true}, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// ListMessages is the newest messages of a channel (50 by default, at most
// 100), oldest first, direct messages included: your backend owns the
// conversation and may need to moderate it. Pass the oldest id you hold as
// opts.Before to page back; opts may be nil.
func (c *StreamClient) ListMessages(ctx context.Context, channel string, opts *ListMessagesOptions) ([]ChatMessage, error) {
	ch, err := segment("channel", channel)
	if err != nil {
		return nil, err
	}
	var query []string
	if opts != nil {
		if opts.Limit != 0 {
			query = append(query, "limit="+encodeFormValue(strconv.Itoa(opts.Limit)))
		}
		if opts.Before != "" {
			query = append(query, "before="+encodeFormValue(opts.Before))
		}
	}
	path := "/v1/chat/" + ch + "/messages"
	if len(query) > 0 {
		path += "?" + strings.Join(query, "&")
	}
	var wrapper struct {
		Messages []ChatMessage `json:"messages"`
	}
	if _, err := c.do(ctx, call{method: "GET", path: path, safeToRepeat: true}, &wrapper); err != nil {
		return nil, err
	}
	return orEmpty(wrapper.Messages), nil
}

// SendMessage speaks from your backend: a system notice, a gift announcement,
// a warning. It skips the per-socket rate limit and the mute list, both of
// which exist to restrain clients. Sent once, never retried: a lost reply
// would become the same message twice.
func (c *StreamClient) SendMessage(ctx context.Context, params SendMessageParams) (*ChatMessage, error) {
	channel, err := segment("channel", params.Channel)
	if err != nil {
		return nil, err
	}
	var message ChatMessage
	if _, err := c.do(ctx, call{method: "POST", path: "/v1/chat/" + channel + "/messages", body: params, hasBody: true}, &message); err != nil {
		return nil, err
	}
	return &message, nil
}

// DeleteMessage removes a message. Everyone who could see it is told to drop it.
func (c *StreamClient) DeleteMessage(ctx context.Context, channel, messageID string) error {
	ch, err := segment("channel", channel)
	if err != nil {
		return err
	}
	id, err := segment("messageId", messageID)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, call{method: "DELETE", path: "/v1/chat/" + ch + "/messages/" + id, safeToRepeat: true, goneIsDone: true}, nil)
	return err
}

// ListChatMutes lists the identities currently muted in a channel's chat.
func (c *StreamClient) ListChatMutes(ctx context.Context, channel string) ([]string, error) {
	ch, err := segment("channel", channel)
	if err != nil {
		return nil, err
	}
	var wrapper struct {
		Muted []string `json:"muted"`
	}
	if _, err := c.do(ctx, call{method: "GET", path: "/v1/chat/" + ch + "/mutes", safeToRepeat: true}, &wrapper); err != nil {
		return nil, err
	}
	return orEmpty(wrapper.Muted), nil
}

// MuteChat silences an identity in a channel's chat. Held for the channel, not
// the connection, so reconnecting does not undo it. They can still read.
func (c *StreamClient) MuteChat(ctx context.Context, channel, identity string) error {
	ch, err := segment("channel", channel)
	if err != nil {
		return err
	}
	who, err := segment("identity", identity)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, call{method: "PUT", path: "/v1/chat/" + ch + "/mutes/" + who, safeToRepeat: true}, nil)
	return err
}

// UnmuteChat lifts a mute.
func (c *StreamClient) UnmuteChat(ctx context.Context, channel, identity string) error {
	ch, err := segment("channel", channel)
	if err != nil {
		return err
	}
	who, err := segment("identity", identity)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, call{method: "DELETE", path: "/v1/chat/" + ch + "/mutes/" + who, safeToRepeat: true, goneIsDone: true}, nil)
	return err
}

// --- broadcast ----------------------------------------------------------------
//
// One publisher, any number of viewers, over LL-HLS and WebRTC. Not offered on
// VoltKit cloud today: every call answers broadcast-disabled (404) on a
// deployment without a media server.

// CreateStream creates a stream. The reply's Publish.WHIPURL is complete:
// hand it to the encoder as it is. This is the only call that returns a
// publish token; reading or listing a stream never does. Sent once, never
// retried, for the reason CreateRoom is not.
func (c *StreamClient) CreateStream(ctx context.Context, params CreateStreamParams) (*BroadcastStreamWithPublish, error) {
	var stream BroadcastStreamWithPublish
	if _, err := c.do(ctx, call{method: "POST", path: "/v1/streams", body: params, hasBody: true}, &stream); err != nil {
		return nil, err
	}
	return &stream, nil
}

// GetStream reads a stream: its state, viewers and playback URLs.
func (c *StreamClient) GetStream(ctx context.Context, streamID string) (*BroadcastStream, error) {
	id, err := segment("streamId", streamID)
	if err != nil {
		return nil, err
	}
	var stream BroadcastStream
	if _, err := c.do(ctx, call{method: "GET", path: "/v1/streams/" + id, safeToRepeat: true}, &stream); err != nil {
		return nil, err
	}
	return &stream, nil
}

// ListStreams lists the project's streams.
func (c *StreamClient) ListStreams(ctx context.Context) ([]BroadcastStream, error) {
	var wrapper struct {
		Streams []BroadcastStream `json:"streams"`
	}
	if _, err := c.do(ctx, call{method: "GET", path: "/v1/streams", safeToRepeat: true}, &wrapper); err != nil {
		return nil, err
	}
	return orEmpty(wrapper.Streams), nil
}

// UpdateStream changes who may watch, or the stream's metadata.
func (c *StreamClient) UpdateStream(ctx context.Context, streamID string, patch UpdateStreamParams) (*BroadcastStream, error) {
	id, err := segment("streamId", streamID)
	if err != nil {
		return nil, err
	}
	var stream BroadcastStream
	if _, err := c.do(ctx, call{method: "PATCH", path: "/v1/streams/" + id, body: patch, hasBody: true, safeToRepeat: true}, &stream); err != nil {
		return nil, err
	}
	return &stream, nil
}

// DeleteStream deletes a stream. Its publish and playback URLs stop working
// at once.
func (c *StreamClient) DeleteStream(ctx context.Context, streamID string) error {
	id, err := segment("streamId", streamID)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, call{method: "DELETE", path: "/v1/streams/" + id, safeToRepeat: true, goneIsDone: true}, nil)
	return err
}

// CreateStreamToken mints a playback token for a viewer of a token-protected
// stream, or a fresh publish token for an encoder whose URL has expired. The
// reply carries the URLs the token belongs in.
func (c *StreamClient) CreateStreamToken(ctx context.Context, params CreateStreamTokenParams) (*StreamToken, error) {
	id, err := segment("streamId", params.StreamID)
	if err != nil {
		return nil, err
	}
	var token StreamToken
	if _, err := c.do(ctx, call{method: "POST", path: "/v1/streams/" + id + "/tokens", body: params, hasBody: true, safeToRepeat: true}, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

// --- usage --------------------------------------------------------------------

// GetUsage is the project's balance of minutes, and its usage for today, this
// month and lifetime.
func (c *StreamClient) GetUsage(ctx context.Context) (*UsageSummary, error) {
	var usage UsageSummary
	if _, err := c.do(ctx, call{method: "GET", path: "/v1/usage", safeToRepeat: true}, &usage); err != nil {
		return nil, err
	}
	return &usage, nil
}

// GetUsageSeries is one entry per UTC day, oldest first, ending today. Days
// with no usage are present and zero, so a chart drawn from it has no gaps.
// days of 0 means the default, 30; the server caps it at 90.
func (c *StreamClient) GetUsageSeries(ctx context.Context, days int) ([]DailyUsage, error) {
	if days == 0 {
		days = 30
	}
	var wrapper struct {
		Series []DailyUsage `json:"series"`
	}
	path := "/v1/usage/series?days=" + encodeSegment(strconv.Itoa(days))
	if _, err := c.do(ctx, call{method: "GET", path: path, safeToRepeat: true}, &wrapper); err != nil {
		return nil, err
	}
	return orEmpty(wrapper.Series), nil
}

// --- billing ------------------------------------------------------------------

// GetBalance is what the project can spend right now, and whether a newcomer
// would be admitted. Read only: recharges happen on the website, never through
// this API. Amounts are cu as decimal strings; use ParseCu for arithmetic.
func (c *StreamClient) GetBalance(ctx context.Context) (*ProjectBalance, error) {
	var balance ProjectBalance
	if _, err := c.do(ctx, call{method: "GET", path: "/v1/billing/balance", safeToRepeat: true}, &balance); err != nil {
		return nil, err
	}
	return &balance, nil
}

// GetRates is the public price list. It comes from the website API
// (Config.RatesBaseURL), where prices are set, rather than the controller, and
// carries no credential: it is public and on another host. Without
// RatesBaseURL it fails at once with rates-unconfigured and sends nothing.
func (c *StreamClient) GetRates(ctx context.Context) (*RtcRates, error) {
	if c.s.ratesBaseURL() == "" {
		return nil, &Error{
			Code:      "rates-unconfigured",
			Status:    0,
			Message:   "GetRates needs Config.RatesBaseURL: the website API origin that serves the public rates, e.g. https://api.example.com",
			Retryable: false,
		}
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	status, err := c.do(ctx, call{method: "GET", path: ratesPath, safeToRepeat: true, public: true}, &envelope)
	if err != nil {
		return nil, err
	}
	data := bytes.TrimSpace(envelope.Data)
	if len(data) == 0 || data[0] != '{' {
		// Not the API's { success, data } envelope: RatesBaseURL most likely
		// names some other service, and asking again gets the same answer.
		return nil, &Error{
			Code:      fmt.Sprintf("http-%d", status),
			Status:    status,
			Message:   fmt.Sprintf("GET %s: the reply carries no rates; is Config.RatesBaseURL the website API origin?", ratesPath),
			Retryable: false,
		}
	}
	var rates RtcRates
	if err := json.Unmarshal(data, &rates); err != nil {
		return nil, &Error{
			Code:      fmt.Sprintf("http-%d", status),
			Status:    status,
			Message:   fmt.Sprintf("GET %s: the reply could not be decoded: %s", ratesPath, c.s.redact(err.Error())),
			Retryable: false,
		}
	}
	return &rates, nil
}

// orEmpty turns a missing list into an empty one: the four list calls and
// GetUsageSeries answer [] when the wrapper has no list (spec section 6).
func orEmpty[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}
